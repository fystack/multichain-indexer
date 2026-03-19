package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/xrp"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const xrpEpochOffset = 946684800

type XRPIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[xrp.XRPLAPI]
	pubkeyStore PubkeyStore
}

func NewXRPIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[xrp.XRPLAPI],
	pubkeyStore PubkeyStore,
) *XRPIndexer {
	return &XRPIndexer{
		chainName:   chainName,
		config:      cfg,
		failover:    failover,
		pubkeyStore: pubkeyStore,
	}
}

func (x *XRPIndexer) GetName() string                  { return strings.ToUpper(x.chainName) }
func (x *XRPIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeXRP }
func (x *XRPIndexer) GetNetworkInternalCode() string   { return x.config.InternalCode }

func (x *XRPIndexer) isMonitoredTransfer(from, to string) bool {
	if x.pubkeyStore == nil {
		return true
	}

	if candidate := normalizeXRPAddress(to); candidate != "" && x.pubkeyStore.Exist(enum.NetworkTypeXRP, candidate) {
		return true
	}

	if !x.config.TwoWayIndexing {
		return false
	}

	candidate := normalizeXRPAddress(from)
	return candidate != "" && x.pubkeyStore.Exist(enum.NetworkTypeXRP, candidate)
}

func (x *XRPIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := x.failover.ExecuteWithRetry(ctx, func(client xrp.XRPLAPI) error {
		n, err := client.GetLatestLedgerIndex(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (x *XRPIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	var ledger *xrp.Ledger
	err := x.failover.ExecuteWithRetry(ctx, func(client xrp.XRPLAPI) error {
		l, err := client.GetLedgerByIndex(ctx, number)
		ledger = l
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get xrp ledger %d failed: %w", number, err)
	}
	if ledger == nil {
		return nil, fmt.Errorf("xrp ledger %d not found", number)
	}
	return x.convertLedger(ledger, number)
}

func (x *XRPIndexer) GetBlocks(ctx context.Context, from, to uint64, isParallel bool) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: from %d > to %d", from, to)
	}
	blockNumbers := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		blockNumbers = append(blockNumbers, n)
	}
	return x.GetBlocksByNumbers(ctx, blockNumbers)
}

func (x *XRPIndexer) GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}

	results := make([]BlockResult, len(blockNumbers))
	indexByLedger := make(map[uint64]int, len(blockNumbers))
	for i, ledgerIndex := range blockNumbers {
		indexByLedger[ledgerIndex] = i
	}

	var ledgers map[uint64]*xrp.Ledger
	err := x.failover.ExecuteWithRetry(ctx, func(client xrp.XRPLAPI) error {
		var err error
		ledgers, err = client.BatchGetLedgersByIndex(ctx, blockNumbers)
		return err
	})
	if err == nil {
		for _, ledgerIndex := range blockNumbers {
			ledger := ledgers[ledgerIndex]
			idx := indexByLedger[ledgerIndex]
			if ledger == nil {
				results[idx] = BlockResult{
					Number: ledgerIndex,
					Error:  &Error{ErrorType: ErrorTypeBlockNotFound, Message: "ledger not found"},
				}
				continue
			}
			block, convertErr := x.convertLedger(ledger, ledgerIndex)
			results[idx] = BlockResult{Number: ledgerIndex, Block: block}
			if convertErr != nil {
				results[idx].Error = &Error{ErrorType: classifyXRPError(convertErr), Message: convertErr.Error()}
			}
		}
		return results, firstBlockError(results)
	}

	workers := x.config.Throttle.Concurrency
	if workers <= 0 {
		workers = 1
	}
	workers = min(workers, len(blockNumbers))

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
				block, getErr := x.GetBlock(ctx, j.num)
				results[j.index] = BlockResult{Number: j.num, Block: block}
				if getErr != nil {
					results[j.index].Error = &Error{
						ErrorType: classifyXRPError(getErr),
						Message:   getErr.Error(),
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
	return results, firstBlockError(results)
}

func (x *XRPIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := x.GetLatestBlockNumber(ctx)
	return err == nil
}

func (x *XRPIndexer) convertLedger(ledger *xrp.Ledger, fallbackNumber uint64) (*types.Block, error) {
	if ledger == nil {
		return nil, fmt.Errorf("xrp ledger is nil")
	}

	number := fallbackNumber
	if strings.TrimSpace(ledger.LedgerIndex.String()) != "" {
		parsed, err := strconv.ParseUint(ledger.LedgerIndex.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid xrp ledger_index %q: %w", ledger.LedgerIndex.String(), err)
		}
		number = parsed
	}

	timestamp := xrpLedgerTime(ledger.CloseTime)
	txs := make([]types.Transaction, 0, len(ledger.Transactions))
	for _, tx := range ledger.Transactions {
		converted, ok := x.convertTransaction(tx, number, timestamp)
		if !ok {
			continue
		}
		txs = append(txs, converted)
	}

	return &types.Block{
		Number:       number,
		Hash:         ledger.LedgerHash,
		ParentHash:   ledger.ParentHash,
		Timestamp:    timestamp,
		Transactions: txs,
	}, nil
}

func (x *XRPIndexer) convertTransaction(tx xrp.Transaction, ledgerIndex uint64, ledgerTimestamp uint64) (types.Transaction, bool) {
	if !strings.EqualFold(strings.TrimSpace(tx.TransactionType), "Payment") {
		return types.Transaction{}, false
	}

	meta := tx.Meta
	if meta == nil {
		meta = tx.MetaData
	}
	if meta == nil || !strings.EqualFold(strings.TrimSpace(meta.TransactionResult), "tesSUCCESS") {
		return types.Transaction{}, false
	}

	from := strings.TrimSpace(tx.Account)
	to := strings.TrimSpace(tx.Destination)
	if from == "" || to == "" {
		return types.Transaction{}, false
	}
	if !x.isMonitoredTransfer(from, to) {
		return types.Transaction{}, false
	}

	amount, txType, assetAddress, ok := parseXRPAmount(deliveredAmount(meta, tx.Amount))
	if !ok {
		return types.Transaction{}, false
	}

	timestamp := ledgerTimestamp
	if tx.Date != nil && *tx.Date > 0 {
		timestamp = xrpLedgerTime(*tx.Date)
	}

	destinationTag := ""
	if tx.DestinationTag != nil {
		destinationTag = strconv.FormatUint(uint64(*tx.DestinationTag), 10)
	}

	return types.Transaction{
		TxHash:         strings.TrimSpace(tx.Hash),
		NetworkId:      x.config.NetworkId,
		BlockNumber:    ledgerIndex,
		FromAddress:    normalizeXRPAddress(from),
		ToAddress:      normalizeXRPAddress(to),
		AssetAddress:   assetAddress,
		DestinationTag: destinationTag,
		Amount:         amount,
		Type:           txType,
		TxFee:          dropsToXRP(strings.TrimSpace(tx.Fee)),
		Timestamp:      timestamp,
		Confirmations:  1,
		Status:         types.StatusConfirmed,
	}, true
}

func classifyXRPError(err error) ErrorType {
	if err == nil {
		return ErrorTypeUnknown
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		return ErrorTypeBlockNotFound
	case strings.Contains(msg, "timeout"):
		return ErrorTypeTimeout
	default:
		return ErrorTypeUnknown
	}
}

func deliveredAmount(meta *xrp.Meta, fallback any) any {
	if meta == nil || meta.DeliveredAmount == nil {
		return fallback
	}
	return meta.DeliveredAmount
}

func parseXRPAmount(raw any) (amount string, txType constant.TxType, assetAddress string, ok bool) {
	switch value := raw.(type) {
	case string:
		return dropsToXRP(value).String(), constant.TxTypeNativeTransfer, "", true
	case map[string]any:
		var parsed xrp.IssuedCurrencyAmount
		data, err := json.Marshal(value)
		if err != nil {
			return "", "", "", false
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return "", "", "", false
		}
		if strings.TrimSpace(parsed.Issuer) == "" || strings.TrimSpace(parsed.Currency) == "" || strings.TrimSpace(parsed.Value) == "" {
			return "", "", "", false
		}
		return strings.TrimSpace(parsed.Value), constant.TxTypeTokenTransfer, formatXRPIssuedCurrency(parsed.Issuer, parsed.Currency), true
	case xrp.IssuedCurrencyAmount:
		if strings.TrimSpace(value.Issuer) == "" || strings.TrimSpace(value.Currency) == "" || strings.TrimSpace(value.Value) == "" {
			return "", "", "", false
		}
		return strings.TrimSpace(value.Value), constant.TxTypeTokenTransfer, formatXRPIssuedCurrency(value.Issuer, value.Currency), true
	default:
		return "", "", "", false
	}
}

func formatXRPIssuedCurrency(issuer string, currency string) string {
	return normalizeXRPAddress(issuer) + ":" + strings.ToUpper(strings.TrimSpace(currency))
}

func dropsToXRP(drops string) decimal.Decimal {
	if strings.TrimSpace(drops) == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(strings.TrimSpace(drops)).Div(decimal.NewFromInt(1_000_000))
}

func xrpLedgerTime(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v + xrpEpochOffset)
}

func normalizeXRPAddress(address string) string {
	return strings.TrimSpace(address)
}

func firstBlockError(results []BlockResult) error {
	for _, result := range results {
		if result.Error != nil {
			return fmt.Errorf("block %d: %s", result.Number, result.Error.Message)
		}
	}
	return nil
}
