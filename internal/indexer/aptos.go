package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
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

	aptosFnTransfer           = "0x1::aptos_account::transfer"
	aptosFnTransferCoins      = "0x1::aptos_account::transfer_coins"
	aptosFnBatchTransferCoins = "0x1::aptos_account::batch_transfer_coins"
	aptosFnTransferFA         = "0x1::aptos_account::transfer_fungible_assets"
	aptosFnCoinTransfer       = "0x1::coin::transfer"

	aptosNativeTypeTag         = "0x1::aptos_coin::aptoscoin"
	aptosNativeMetadataAddress = "0xa"

	aptosCoinDepositEventType   = "0x1::coin::depositevent"
	aptosCoinWithdrawEventType  = "0x1::coin::withdrawevent"
	aptosFADepositEventType     = "0x1::fungible_asset::deposit"
	aptosFAWithdrawEventType    = "0x1::fungible_asset::withdraw"
	aptosCoinStoreResourceType  = "0x1::coin::coinstore"
	aptosFStoreResourceType     = "0x1::fungible_asset::fungiblestore"
	aptosObjectCoreResourceType = "0x1::object::objectcore"
)

type AptosIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[aptos.AptosAPI]
	pubkeyStore PubkeyStore
}

type aptosAssetInfo struct {
	TxType       constant.TxType
	AssetAddress string
}

type aptosEventMovement struct {
	IsWithdraw     bool
	Address        string
	Amount         *big.Int
	SequenceNumber string
	EventIndex     int
	Asset          aptosAssetInfo
}

type aptosMovementGroup struct {
	Withdrawals []aptosEventMovement
	Deposits    []aptosEventMovement
}

type aptosTransferCandidate struct {
	Tx         types.Transaction
	OrderIndex int
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

func (a *AptosIndexer) isMonitoredTransfer(from, to string) bool {
	if a.pubkeyStore == nil {
		return true
	}

	if a.isMonitoredAddress(to) {
		return true
	}

	return a.config.TwoWayIndexing && a.isMonitoredAddress(from)
}

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
	for txIndex, tx := range blockData.Transactions {
		parsedTransfers := a.extractTransfers(tx, blockHeight, blockTs)
		for transferIndex, parsed := range parsedTransfers {
			parsed.BlockHash = blockData.BlockHash
			parsed.TransferIndex = formatAptosTransferIndex(
				txIndex,
				transferIndex,
				parsed.TransferIndex,
			)
			if !a.isMonitoredTransfer(parsed.FromAddress, parsed.ToAddress) {
				continue
			}
			txs = append(txs, parsed)
		}
	}

	return &types.Block{
		Number:       blockHeight,
		Hash:         blockData.BlockHash,
		ParentHash:   "",
		Timestamp:    blockTs,
		Transactions: txs,
	}, nil
}

func (a *AptosIndexer) extractTransfers(
	tx aptos.Transaction,
	blockHeight, blockTs uint64,
) []types.Transaction {
	if strings.ToLower(strings.TrimSpace(tx.Type)) != "user_transaction" {
		return nil
	}
	if !tx.Success {
		return nil
	}

	movements := extractAptosEventMovements(tx)
	if len(movements) == 0 {
		return nil
	}

	timestamp := parseAptosTimestamp(tx.Timestamp, blockTs)
	fee := convertAptosFeeToNative(tx.GasUsed, tx.GasUnitPrice)
	baseTx := types.Transaction{
		TxHash:      tx.Hash,
		NetworkId:   a.config.NetworkId,
		BlockNumber: blockHeight,
		TxFee:       fee,
		Timestamp:   timestamp,
	}

	return buildAptosTransfersFromMovements(baseTx, movements)
}

func extractAptosEventMovements(tx aptos.Transaction) []aptosEventMovement {
	coinEventStreams := parseAptosCoinEventStreams(tx.Changes)
	storeOwners, storeMetadata := parseAptosFungibleStoreIndexes(tx.Changes)
	fallbackAsset, hasFallbackAsset := inferAptosAssetFromPayload(tx.Payload)

	movements := make([]aptosEventMovement, 0, len(tx.Events))
	for idx, evt := range tx.Events {
		eventType := normalizeAptosTypeTag(evt.Type)
		switch eventType {
		case aptosCoinDepositEventType, aptosCoinWithdrawEventType:
			account := normalizeAptosAddress(evt.GUID.AccountAddress)
			if account == "" {
				continue
			}
			amount, ok := parseAptosEventAmount(evt.Data)
			if !ok {
				continue
			}

			asset := coinEventStreams[coinEventStreamKey(account, evt.GUID.CreationNumber)]
			if asset.TxType == "" && hasFallbackAsset {
				asset = fallbackAsset
			}
			if asset.TxType == "" {
				asset = aptosAssetInfo{TxType: constant.TxTypeNativeTransfer}
			}

			movements = append(movements, aptosEventMovement{
				IsWithdraw:     eventType == aptosCoinWithdrawEventType,
				Address:        account,
				Amount:         amount,
				SequenceNumber: strings.TrimSpace(evt.SequenceNumber),
				EventIndex:     idx,
				Asset:          normalizeAptosAsset(asset),
			})

		case aptosFADepositEventType, aptosFAWithdrawEventType:
			store, ok := parseAptosEventAddressField(evt.Data, "store")
			if !ok {
				continue
			}

			owner := storeOwners[store]
			if owner == "" {
				owner = store
			}
			amount, ok := parseAptosEventAmount(evt.Data)
			if !ok {
				continue
			}

			asset := aptosAssetInfo{
				TxType:       constant.TxTypeTokenTransfer,
				AssetAddress: storeMetadata[store],
			}
			if asset.AssetAddress == "" && hasFallbackAsset {
				asset = fallbackAsset
			}

			movements = append(movements, aptosEventMovement{
				IsWithdraw:     eventType == aptosFAWithdrawEventType,
				Address:        owner,
				Amount:         amount,
				SequenceNumber: strings.TrimSpace(evt.SequenceNumber),
				EventIndex:     idx,
				Asset:          normalizeAptosAsset(asset),
			})
		}
	}
	return movements
}

func buildAptosTransfersFromMovements(baseTx types.Transaction, movements []aptosEventMovement) []types.Transaction {
	if len(movements) == 0 {
		return nil
	}

	groups := make(map[string]*aptosMovementGroup)
	for _, movement := range movements {
		if movement.Amount == nil || movement.Amount.Sign() <= 0 {
			continue
		}
		if movement.Address == "" {
			continue
		}

		movement.Asset = normalizeAptosAsset(movement.Asset)
		key := aptosMovementGroupKey(movement.Asset)
		group := groups[key]
		if group == nil {
			group = &aptosMovementGroup{}
			groups[key] = group
		}

		if movement.IsWithdraw {
			group.Withdrawals = append(group.Withdrawals, movement)
		} else {
			group.Deposits = append(group.Deposits, movement)
		}
	}

	candidates := make([]aptosTransferCandidate, 0)
	for _, group := range groups {
		if len(group.Withdrawals) == 0 || len(group.Deposits) == 0 {
			continue
		}

		sort.SliceStable(group.Withdrawals, func(i, j int) bool {
			return group.Withdrawals[i].EventIndex < group.Withdrawals[j].EventIndex
		})
		sort.SliceStable(group.Deposits, func(i, j int) bool {
			return group.Deposits[i].EventIndex < group.Deposits[j].EventIndex
		})

		withdrawals := cloneAptosMovements(group.Withdrawals)
		deposits := cloneAptosMovements(group.Deposits)

		wIdx, dIdx := 0, 0
		for wIdx < len(withdrawals) && dIdx < len(deposits) {
			withdraw := &withdrawals[wIdx]
			deposit := &deposits[dIdx]
			if withdraw.Amount.Sign() <= 0 {
				wIdx++
				continue
			}
			if deposit.Amount.Sign() <= 0 {
				dIdx++
				continue
			}

			amount := minAptosAmount(withdraw.Amount, deposit.Amount)
			transfer := baseTx
			transfer.FromAddress = withdraw.Address
			transfer.ToAddress = deposit.Address
			transfer.Amount = amount.String()
			transfer.Type = withdraw.Asset.TxType
			transfer.AssetAddress = withdraw.Asset.AssetAddress
			transfer.TransferIndex = buildAptosTransferIndex(*withdraw, *deposit)
			transfer.AssetAddress = normalizeAssetAddressForTxType(transfer.Type, transfer.AssetAddress)

			candidates = append(candidates, aptosTransferCandidate{
				Tx:         transfer,
				OrderIndex: min(withdraw.EventIndex, deposit.EventIndex),
			})

			withdraw.Amount.Sub(withdraw.Amount, amount)
			deposit.Amount.Sub(deposit.Amount, amount)
			if withdraw.Amount.Sign() == 0 {
				wIdx++
			}
			if deposit.Amount.Sign() == 0 {
				dIdx++
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].OrderIndex != candidates[j].OrderIndex {
			return candidates[i].OrderIndex < candidates[j].OrderIndex
		}
		return candidates[i].Tx.TransferIndex < candidates[j].Tx.TransferIndex
	})

	txs := make([]types.Transaction, 0, len(candidates))
	for _, candidate := range candidates {
		txs = append(txs, candidate.Tx)
	}
	return txs
}

func cloneAptosMovements(src []aptosEventMovement) []aptosEventMovement {
	out := make([]aptosEventMovement, len(src))
	for i := range src {
		out[i] = src[i]
		if src[i].Amount != nil {
			out[i].Amount = new(big.Int).Set(src[i].Amount)
		}
	}
	return out
}

func minAptosAmount(a, b *big.Int) *big.Int {
	if a.Cmp(b) <= 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func buildAptosTransferIndex(withdraw, deposit aptosEventMovement) string {
	withdrawSeq := strings.TrimSpace(withdraw.SequenceNumber)
	depositSeq := strings.TrimSpace(deposit.SequenceNumber)
	if withdrawSeq == "" {
		withdrawSeq = "na"
	}
	if depositSeq == "" {
		depositSeq = "na"
	}
	return fmt.Sprintf(
		"seq:%s:%s:%d:%d",
		withdrawSeq,
		depositSeq,
		withdraw.EventIndex,
		deposit.EventIndex,
	)
}

func parseAptosCoinEventStreams(changes []aptos.WriteSetChange) map[string]aptosAssetInfo {
	streams := make(map[string]aptosAssetInfo)
	for _, change := range changes {
		if strings.ToLower(strings.TrimSpace(change.Type)) != "write_resource" || change.Data == nil {
			continue
		}

		coinType, ok := parseAptosCoinTypeFromResourceType(change.Data.Type)
		if !ok {
			continue
		}

		account := normalizeAptosAddress(change.Address)
		if account == "" {
			continue
		}

		asset := classifyAptosCoinType(coinType)
		if creation, ok := parseAptosEventHandleCreationNumber(change.Data.Data, "deposit_events"); ok {
			streams[coinEventStreamKey(account, creation)] = asset
		}
		if creation, ok := parseAptosEventHandleCreationNumber(change.Data.Data, "withdraw_events"); ok {
			streams[coinEventStreamKey(account, creation)] = asset
		}
	}
	return streams
}

func parseAptosFungibleStoreIndexes(changes []aptos.WriteSetChange) (map[string]string, map[string]string) {
	storeOwners := make(map[string]string)
	storeMetadata := make(map[string]string)

	for _, change := range changes {
		if strings.ToLower(strings.TrimSpace(change.Type)) != "write_resource" || change.Data == nil {
			continue
		}
		resourceType := normalizeAptosTypeTag(change.Data.Type)
		address := normalizeAptosAddress(change.Address)
		if address == "" {
			continue
		}

		switch resourceType {
		case aptosObjectCoreResourceType:
			if owner, ok := parseAptosAddressField(change.Data.Data, "owner"); ok {
				storeOwners[address] = owner
			}
		case aptosFStoreResourceType:
			if metadata, ok := parseAptosAddressLikeField(change.Data.Data, "metadata"); ok {
				storeMetadata[address] = metadata
			}
		}
	}

	return storeOwners, storeMetadata
}

func parseAptosCoinTypeFromResourceType(resourceType string) (string, bool) {
	resourceType = normalizeAptosTypeTag(resourceType)
	prefix := aptosCoinStoreResourceType + "<"
	if !strings.HasPrefix(resourceType, prefix) || !strings.HasSuffix(resourceType, ">") {
		return "", false
	}

	coinType := strings.TrimSpace(
		strings.TrimSuffix(strings.TrimPrefix(resourceType, prefix), ">"),
	)
	if coinType == "" {
		return "", false
	}
	return normalizeAptosTypeTag(coinType), true
}

func parseAptosEventHandleCreationNumber(data map[string]json.RawMessage, field string) (string, bool) {
	raw, ok := data[field]
	if !ok {
		return "", false
	}

	var handle map[string]json.RawMessage
	if err := json.Unmarshal(raw, &handle); err != nil {
		return "", false
	}

	if guidRaw, ok := handle["guid"]; ok {
		var guid map[string]json.RawMessage
		if err := json.Unmarshal(guidRaw, &guid); err == nil {
			if idRaw, ok := guid["id"]; ok {
				var id map[string]json.RawMessage
				if err := json.Unmarshal(idRaw, &id); err == nil {
					if creation, ok := parseAptosCreationNumber(id); ok {
						return creation, true
					}
				}
			}
			if creation, ok := parseAptosCreationNumber(guid); ok {
				return creation, true
			}
		}
	}

	return parseAptosCreationNumber(handle)
}

func parseAptosCreationNumber(obj map[string]json.RawMessage) (string, bool) {
	for _, key := range []string{"creation_num", "creation_number"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		if creation, ok := parseAptosAmountArg(raw); ok {
			return creation, true
		}
	}
	return "", false
}

func coinEventStreamKey(account, creationNumber string) string {
	return normalizeAptosAddress(account) + "|" + strings.TrimSpace(creationNumber)
}

func parseAptosEventAddressField(data aptos.EventData, key string) (string, bool) {
	raw, ok := data[key]
	if !ok {
		return "", false
	}
	return parseAptosAddressLikeArg(raw)
}

func parseAptosEventAmount(data aptos.EventData) (*big.Int, bool) {
	raw, ok := data["amount"]
	if !ok {
		return nil, false
	}
	amount, ok := parseAptosAmountArg(raw)
	if !ok {
		return nil, false
	}
	value, ok := parseBigInt(amount)
	if !ok || value.Sign() <= 0 {
		return nil, false
	}
	return value, true
}

func parseAptosAddressField(data map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := data[key]
	if !ok {
		return "", false
	}
	return parseAptosAddressArg(raw)
}

func parseAptosAddressLikeField(data map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := data[key]
	if !ok {
		return "", false
	}
	return parseAptosAddressLikeArg(raw)
}

func inferAptosAssetFromPayload(payload *aptos.TransactionPayload) (aptosAssetInfo, bool) {
	if payload == nil || strings.ToLower(strings.TrimSpace(payload.Type)) != aptosEntryFunctionPayload {
		return aptosAssetInfo{}, false
	}

	function := normalizeAptosFunction(payload.Function)
	if !isAptosTransferFunction(function) {
		return aptosAssetInfo{}, false
	}

	var faMetadataAddress string
	if function == aptosFnTransferFA {
		var ok bool
		_, _, faMetadataAddress, ok = parseAptosTransferArgs(function, payload.Arguments)
		if !ok {
			return aptosAssetInfo{}, false
		}
	}

	txType, assetAddress := classifyAptosTransfer(function, payload.TypeArguments, faMetadataAddress)
	return normalizeAptosAsset(aptosAssetInfo{
		TxType:       txType,
		AssetAddress: assetAddress,
	}), true
}

func classifyAptosCoinType(coinType string) aptosAssetInfo {
	coinType = normalizeAptosTypeTag(coinType)
	if coinType == "" || coinType == aptosNativeTypeTag {
		return aptosAssetInfo{
			TxType: constant.TxTypeNativeTransfer,
		}
	}
	return aptosAssetInfo{
		TxType:       constant.TxTypeTokenTransfer,
		AssetAddress: coinType,
	}
}

func normalizeAptosAsset(asset aptosAssetInfo) aptosAssetInfo {
	if asset.TxType == "" {
		if strings.TrimSpace(asset.AssetAddress) == "" {
			asset.TxType = constant.TxTypeNativeTransfer
		} else {
			asset.TxType = constant.TxTypeTokenTransfer
		}
	}

	asset.AssetAddress = strings.TrimSpace(asset.AssetAddress)
	if asset.AssetAddress != "" {
		asset.AssetAddress = normalizeAptosTypeTag(asset.AssetAddress)
		if normalizedAddress := normalizeAptosAddress(asset.AssetAddress); normalizedAddress != "" {
			asset.AssetAddress = normalizedAddress
		}
	}
	if asset.AssetAddress == aptosNativeMetadataAddress {
		asset.TxType = constant.TxTypeNativeTransfer
	}
	if asset.TxType == constant.TxTypeNativeTransfer {
		asset.AssetAddress = ""
	}
	return asset
}

func aptosMovementGroupKey(asset aptosAssetInfo) string {
	return string(asset.TxType) + "|" + asset.AssetAddress
}

func normalizeAssetAddressForTxType(txType constant.TxType, assetAddress string) string {
	if txType == constant.TxTypeNativeTransfer {
		return ""
	}
	return strings.TrimSpace(assetAddress)
}

func formatAptosTransferIndex(txIndex, transferIndex int, rawIndex string) string {
	rawIndex = strings.TrimSpace(rawIndex)
	if rawIndex == "" {
		return fmt.Sprintf("%d:%d", txIndex, transferIndex)
	}
	return fmt.Sprintf("%d:%d:%s", txIndex, transferIndex, rawIndex)
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
	case aptosFnTransfer, aptosFnTransferCoins, aptosFnBatchTransferCoins, aptosFnTransferFA, aptosFnCoinTransfer:
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
