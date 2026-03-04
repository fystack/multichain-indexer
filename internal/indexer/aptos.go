package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/aptos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const (
	aptosOctasPerAPT = int64(100_000_000)

	aptosEntryFunctionPayload = "entry_function_payload"

	aptosFnTransfer      = "0x1::aptos_account::transfer"
	aptosFnTransferCoins = "0x1::aptos_account::transfer_coins"
	aptosFnTransferFA    = "0x1::aptos_account::transfer_fungible_assets"
	aptosFnCoinTransfer  = "0x1::coin::transfer"

	aptosNativeTypeTag = "0x1::aptos_coin::aptoscoin"
)

type AptosIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[aptos.AptosAPI]
	pubkeyStore PubkeyStore
}

func NewAptosIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[aptos.AptosAPI],
	pubkeyStore PubkeyStore,
) *AptosIndexer {
	return &AptosIndexer{
		chainName:   chainName,
		config:      cfg,
		failover:    failover,
		pubkeyStore: pubkeyStore,
	}
}

func (a *AptosIndexer) GetName() string                  { return strings.ToUpper(a.chainName) }
func (a *AptosIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeApt }
func (a *AptosIndexer) GetNetworkInternalCode() string   { return a.config.InternalCode }

func (a *AptosIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := a.failover.ExecuteWithRetry(ctx, func(client aptos.AptosAPI) error {
		n, err := client.GetLatestBlockHeight(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (a *AptosIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	var blockData *aptos.BlockResponse
	err := a.failover.ExecuteWithRetry(ctx, func(client aptos.AptosAPI) error {
		b, err := client.GetBlockByHeight(ctx, number, true)
		blockData = b
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get aptos block %d failed: %w", number, err)
	}
	if blockData == nil {
		return nil, fmt.Errorf("aptos block %d not found", number)
	}
	return a.convertBlock(blockData, number)
}

func (a *AptosIndexer) GetBlocks(
	ctx context.Context,
	from, to uint64,
	isParallel bool,
) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: from %d > to %d", from, to)
	}

	nums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		nums = append(nums, n)
	}

	workers := 1
	if isParallel {
		workers = a.config.Throttle.Concurrency
	}
	return a.getBlocks(ctx, nums, workers)
}

func (a *AptosIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	return a.getBlocks(ctx, blockNumbers, a.config.Throttle.Concurrency)
}

func (a *AptosIndexer) getBlocks(
	ctx context.Context,
	blockNumbers []uint64,
	workers int,
) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}
	if workers <= 0 {
		workers = 1
	}
	workers = min(workers, len(blockNumbers))

	results := make([]BlockResult, len(blockNumbers))

	type job struct {
		index int
		num   uint64
	}

	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				block, err := a.GetBlock(ctx, j.num)
				results[j.index] = BlockResult{
					Number: j.num,
					Block:  block,
				}
				if err != nil {
					results[j.index].Error = &Error{
						ErrorType: classifyAptosError(err),
						Message:   err.Error(),
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i, num := range blockNumbers {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{index: i, num: num}:
			}
		}
	}()

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var firstErr error
	for _, res := range results {
		if res.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", res.Number, res.Error.Message)
			break
		}
	}

	return results, firstErr
}

func (a *AptosIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.GetLatestBlockNumber(ctx)
	return err == nil
}

func (a *AptosIndexer) convertBlock(
	blockData *aptos.BlockResponse,
	fallbackHeight uint64,
) (*types.Block, error) {
	blockHeight := fallbackHeight
	if strings.TrimSpace(blockData.BlockHeight) != "" {
		n, err := strconv.ParseUint(strings.TrimSpace(blockData.BlockHeight), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid block_height %q: %w", blockData.BlockHeight, err)
		}
		blockHeight = n
	}

	blockTs := parseAptosTimestamp(blockData.BlockTimestamp, 0)

	txs := make([]types.Transaction, 0, len(blockData.Transactions))
	for _, tx := range blockData.Transactions {
		parsed, ok := a.extractTransfer(tx, blockHeight, blockTs)
		if !ok {
			continue
		}
		if a.pubkeyStore != nil && !a.isMonitoredAddress(parsed.ToAddress) {
			continue
		}
		txs = append(txs, parsed)
	}

	return &types.Block{
		Number:       blockHeight,
		Hash:         blockData.BlockHash,
		ParentHash:   "",
		Timestamp:    blockTs,
		Transactions: txs,
	}, nil
}

func (a *AptosIndexer) extractTransfer(
	tx aptos.Transaction,
	blockHeight, blockTs uint64,
) (types.Transaction, bool) {
	if strings.ToLower(strings.TrimSpace(tx.Type)) != "user_transaction" {
		return types.Transaction{}, false
	}
	if !tx.Success {
		return types.Transaction{}, false
	}
	if tx.Payload == nil || strings.ToLower(strings.TrimSpace(tx.Payload.Type)) != aptosEntryFunctionPayload {
		return types.Transaction{}, false
	}

	function := normalizeAptosFunction(tx.Payload.Function)
	if !isAptosTransferFunction(function) {
		return types.Transaction{}, false
	}
	toAddress, amount, faMetadataAddress, ok := parseAptosTransferArgs(function, tx.Payload.Arguments)
	if !ok {
		return types.Transaction{}, false
	}

	fromAddress := normalizeAptosAddress(tx.Sender)
	toAddress = normalizeAptosAddress(toAddress)
	if fromAddress == "" || toAddress == "" {
		return types.Transaction{}, false
	}

	txType, assetAddress := classifyAptosTransfer(function, tx.Payload.TypeArguments, faMetadataAddress)
	timestamp := parseAptosTimestamp(tx.Timestamp, blockTs)
	fee := convertAptosFeeToNative(tx.GasUsed, tx.GasUnitPrice)

	return types.Transaction{
		TxHash:        tx.Hash,
		NetworkId:     a.config.NetworkId,
		BlockNumber:   blockHeight,
		FromAddress:   fromAddress,
		ToAddress:     toAddress,
		AssetAddress:  assetAddress,
		Amount:        amount,
		Type:          txType,
		TxFee:         fee,
		Timestamp:     timestamp,
	}, true
}

func (a *AptosIndexer) isMonitoredAddress(address string) bool {
	if a.pubkeyStore == nil {
		return true
	}

	short := normalizeAptosAddress(address)
	long := expandAptosAddress(short)
	raw := strings.TrimPrefix(short, "0x")

	candidates := []string{address, short, long, raw}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if a.pubkeyStore.Exist(enum.NetworkTypeApt, candidate) {
			return true
		}
	}
	return false
}

func classifyAptosError(err error) ErrorType {
	if err == nil {
		return ErrorTypeUnknown
	}
	if aptos.IsNotFoundError(err) {
		return ErrorTypeBlockNotFound
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return ErrorTypeTimeout
	}
	return ErrorTypeUnknown
}

func isAptosTransferFunction(function string) bool {
	switch function {
	case aptosFnTransfer, aptosFnTransferCoins, aptosFnTransferFA, aptosFnCoinTransfer:
		return true
	default:
		return false
	}
}

func classifyAptosTransfer(function string, typeArgs []string, faMetadataAddress string) (constant.TxType, string) {
	if function == aptosFnTransfer {
		return constant.TxTypeNativeTransfer, ""
	}
	if function == aptosFnTransferFA {
		return constant.TxTypeTokenTransfer, faMetadataAddress
	}

	var coinType string
	if len(typeArgs) > 0 {
		coinType = normalizeAptosTypeTag(typeArgs[0])
	}
	if coinType == "" || coinType == aptosNativeTypeTag {
		return constant.TxTypeNativeTransfer, ""
	}
	return constant.TxTypeTokenTransfer, coinType
}

func parseAptosTransferArgs(
	function string,
	args []json.RawMessage,
) (toAddress, amount, faMetadataAddress string, ok bool) {
	if function == aptosFnTransferFA {
		if len(args) < 3 {
			return "", "", "", false
		}
		faMetadataAddress, _ = parseAptosAddressLikeArg(args[0])
		toAddress, ok = parseAptosAddressArg(args[1])
		if !ok {
			return "", "", "", false
		}
		amount, ok = parseAptosAmountArg(args[2])
		if !ok {
			return "", "", "", false
		}
		return toAddress, amount, faMetadataAddress, true
	}

	if len(args) < 2 {
		return "", "", "", false
	}
	toAddress, ok = parseAptosAddressArg(args[0])
	if !ok {
		return "", "", "", false
	}
	amount, ok = parseAptosAmountArg(args[1])
	if !ok {
		return "", "", "", false
	}
	return toAddress, amount, "", true
}

func convertAptosFeeToNative(gasUsed, gasUnitPrice string) decimal.Decimal {
	gasUsedInt, ok := parseBigInt(gasUsed)
	if !ok {
		return decimal.Zero
	}
	gasUnitPriceInt, ok := parseBigInt(gasUnitPrice)
	if !ok {
		return decimal.Zero
	}

	feeOctas := new(big.Int).Mul(gasUsedInt, gasUnitPriceInt)
	return decimal.NewFromBigInt(feeOctas, 0).Div(decimal.NewFromInt(aptosOctasPerAPT))
}

func parseBigInt(raw string) (*big.Int, bool) {
	raw = strings.TrimSpace(raw)
	if !isUnsignedInteger(raw) {
		return nil, false
	}
	out, ok := new(big.Int).SetString(raw, 10)
	return out, ok
}

func parseAptosAddressArg(raw json.RawMessage) (string, bool) {
	var addr string
	if err := json.Unmarshal(raw, &addr); err != nil {
		return "", false
	}
	addr = normalizeAptosAddress(addr)
	if addr == "" {
		return "", false
	}
	return addr, true
}

func parseAptosAddressLikeArg(raw json.RawMessage) (string, bool) {
	if addr, ok := parseAptosAddressArg(raw); ok {
		return addr, true
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", false
	}

	for _, key := range []string{"inner", "metadata", "address"} {
		value, found := obj[key]
		if !found {
			continue
		}
		if addr, ok := parseAptosAddressArg(value); ok {
			return addr, true
		}
	}
	return "", false
}

func parseAptosAmountArg(raw json.RawMessage) (string, bool) {
	var amountStr string
	if err := json.Unmarshal(raw, &amountStr); err == nil {
		amountStr = strings.TrimSpace(amountStr)
		if isUnsignedInteger(amountStr) {
			return amountStr, true
		}
	}

	var amountNum json.Number
	if err := json.Unmarshal(raw, &amountNum); err == nil {
		amountStr = strings.TrimSpace(amountNum.String())
		if isUnsignedInteger(amountStr) {
			return amountStr, true
		}
	}

	return "", false
}

func parseAptosTimestamp(raw string, fallback uint64) uint64 {
	raw = strings.TrimSpace(raw)
	if !isUnsignedInteger(raw) {
		return fallback
	}

	ts, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return fallback
	}

	// Aptos timestamps are typically microseconds. Keep parsing tolerant for seconds/milliseconds.
	switch {
	case ts >= 1_000_000_000_000_000:
		return ts / 1_000_000
	case ts >= 1_000_000_000_000:
		return ts / 1_000
	default:
		return ts
	}
}

func normalizeAptosFunction(function string) string {
	function = strings.TrimSpace(strings.ToLower(function))
	parts := strings.Split(function, "::")
	if len(parts) != 3 {
		return function
	}
	return normalizeAptosAddress(parts[0]) + "::" + parts[1] + "::" + parts[2]
}

func normalizeAptosTypeTag(typeTag string) string {
	typeTag = strings.TrimSpace(strings.ToLower(typeTag))
	parts := strings.Split(typeTag, "::")
	if len(parts) < 3 {
		return typeTag
	}
	parts[0] = normalizeAptosAddress(parts[0])
	return strings.Join(parts, "::")
}

func normalizeAptosAddress(address string) string {
	address = strings.TrimSpace(strings.ToLower(address))
	if address == "" {
		return ""
	}
	address = strings.TrimPrefix(address, "0x")
	if address == "" {
		return ""
	}
	if !isHexString(address) {
		return ""
	}

	address = strings.TrimLeft(address, "0")
	if address == "" {
		address = "0"
	}
	return "0x" + address
}

func expandAptosAddress(address string) string {
	address = normalizeAptosAddress(address)
	if address == "" {
		return ""
	}
	address = strings.TrimPrefix(address, "0x")
	if len(address) > 64 {
		return ""
	}
	return "0x" + strings.Repeat("0", 64-len(address)) + address
}

func isHexString(raw string) bool {
	if raw == "" {
		return false
	}
	for _, ch := range raw {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func isUnsignedInteger(raw string) bool {
	if raw == "" {
		return false
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
