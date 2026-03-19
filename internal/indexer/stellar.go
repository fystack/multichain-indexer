package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/stellar"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const stellarPaymentsPageLimit = 200

type StellarIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[stellar.StellarAPI]
	pubkeyStore PubkeyStore
}

func NewStellarIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[stellar.StellarAPI],
	pubkeyStore PubkeyStore,
) *StellarIndexer {
	return &StellarIndexer{
		chainName:   chainName,
		config:      cfg,
		failover:    failover,
		pubkeyStore: pubkeyStore,
	}
}

func (s *StellarIndexer) GetName() string                  { return strings.ToUpper(s.chainName) }
func (s *StellarIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeStellar }
func (s *StellarIndexer) GetNetworkInternalCode() string   { return s.config.InternalCode }

func (s *StellarIndexer) isMonitoredTransfer(from, to string) bool {
	if s.pubkeyStore == nil {
		return true
	}

	to = normalizeStellarAddress(to)
	if to != "" && s.pubkeyStore.Exist(enum.NetworkTypeStellar, to) {
		return true
	}

	if !s.config.TwoWayIndexing {
		return false
	}

	from = normalizeStellarAddress(from)
	return from != "" && s.pubkeyStore.Exist(enum.NetworkTypeStellar, from)
}

func (s *StellarIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := s.failover.ExecuteWithRetry(ctx, func(client stellar.StellarAPI) error {
		n, err := client.GetLatestLedgerSequence(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (s *StellarIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	var (
		ledger   *stellar.Ledger
		payments []stellar.Payment
	)
	err := s.failover.ExecuteWithRetry(ctx, func(client stellar.StellarAPI) error {
		l, err := client.GetLedger(ctx, number)
		if err != nil {
			return err
		}
		ledger = l

		allPayments, err := fetchStellarLedgerPayments(ctx, client, number)
		if err != nil {
			return err
		}
		payments = allPayments
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get stellar ledger %d failed: %w", number, err)
	}
	if ledger == nil {
		return nil, fmt.Errorf("stellar ledger %d not found", number)
	}
	return s.convertLedger(ctx, ledger, payments)
}

func (s *StellarIndexer) GetBlocks(ctx context.Context, from, to uint64, isParallel bool) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: from %d > to %d", from, to)
	}
	blockNumbers := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		blockNumbers = append(blockNumbers, n)
	}
	return s.GetBlocksByNumbers(ctx, blockNumbers)
}

func (s *StellarIndexer) GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}

	workers := s.config.Throttle.Concurrency
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
				block, err := s.GetBlock(ctx, j.num)
				results[j.index] = BlockResult{Number: j.num, Block: block}
				if err != nil {
					results[j.index].Error = &Error{
						ErrorType: classifyStellarError(err),
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
	return results, firstBlockError(results)
}

func (s *StellarIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.GetLatestBlockNumber(ctx)
	return err == nil
}

func (s *StellarIndexer) convertLedger(ctx context.Context, ledger *stellar.Ledger, payments []stellar.Payment) (*types.Block, error) {
	if ledger == nil {
		return nil, fmt.Errorf("stellar ledger is nil")
	}

	txDetails, err := s.fetchTransactionsForPayments(ctx, payments)
	if err != nil {
		return nil, err
	}

	timestamp, err := parseRFC3339Unix(ledger.ClosedAt)
	if err != nil {
		return nil, fmt.Errorf("parse stellar ledger time: %w", err)
	}

	txs := make([]types.Transaction, 0, len(payments))
	for _, payment := range payments {
		txDetail := txDetails[strings.TrimSpace(payment.TransactionHash)]
		converted, ok := s.convertPayment(payment, txDetail, ledger.Sequence, timestamp)
		if !ok {
			continue
		}
		txs = append(txs, converted)
	}

	return &types.Block{
		Number:       ledger.Sequence,
		Hash:         ledger.Hash,
		ParentHash:   ledger.PrevHash,
		Timestamp:    timestamp,
		Transactions: txs,
	}, nil
}

func (s *StellarIndexer) fetchTransactionsForPayments(ctx context.Context, payments []stellar.Payment) (map[string]*stellar.Transaction, error) {
	hashes := make(map[string]struct{})
	for _, payment := range payments {
		hash := strings.TrimSpace(payment.TransactionHash)
		if hash != "" {
			hashes[hash] = struct{}{}
		}
	}

	txByHash := make(map[string]*stellar.Transaction, len(hashes))
	for hash := range hashes {
		var txDetail *stellar.Transaction
		err := s.failover.ExecuteWithRetry(ctx, func(client stellar.StellarAPI) error {
			tx, err := client.GetTransaction(ctx, hash)
			txDetail = tx
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("get stellar transaction %s failed: %w", hash, err)
		}
		txByHash[hash] = txDetail
	}
	return txByHash, nil
}

func (s *StellarIndexer) convertPayment(payment stellar.Payment, txDetail *stellar.Transaction, ledgerSequence uint64, ledgerTimestamp uint64) (types.Transaction, bool) {
	if !payment.TransactionSuccessful {
		return types.Transaction{}, false
	}
	if !isSupportedStellarPayment(payment.Type) {
		return types.Transaction{}, false
	}

	from, to, amount := stellarTransferFields(payment)
	if from == "" || to == "" || amount == "" {
		return types.Transaction{}, false
	}
	if !s.isMonitoredTransfer(from, to) {
		return types.Transaction{}, false
	}

	assetAddress := ""
	txType := constant.TxTypeNativeTransfer
	if !isNativeStellarPayment(payment) {
		txType = constant.TxTypeTokenTransfer
		assetAddress = formatStellarAsset(payment.AssetIssuer, payment.AssetCode)
		if assetAddress == "" {
			return types.Transaction{}, false
		}
	}

	timestamp := ledgerTimestamp
	if txDetail != nil && strings.TrimSpace(txDetail.CreatedAt) != "" {
		parsed, err := parseRFC3339Unix(txDetail.CreatedAt)
		if err == nil {
			timestamp = parsed
		}
	}

	fee := decimal.Zero
	memo := ""
	memoType := ""
	if txDetail != nil {
		fee = stroopsToXLM(strings.TrimSpace(txDetail.FeeCharged))
		memo = strings.TrimSpace(txDetail.Memo)
		memoType = strings.TrimSpace(txDetail.MemoType)
	}

	return types.Transaction{
		TxHash:        strings.TrimSpace(payment.TransactionHash),
		NetworkId:     s.config.NetworkId,
		BlockNumber:   ledgerSequence,
		FromAddress:   normalizeStellarAddress(from),
		ToAddress:     normalizeStellarAddress(to),
		AssetAddress:  assetAddress,
		Memo:          memo,
		MemoType:      memoType,
		Amount:        amount,
		Type:          txType,
		TxFee:         fee,
		Timestamp:     timestamp,
		Confirmations: 1,
		Status:        types.StatusConfirmed,
	}, true
}

func fetchStellarLedgerPayments(ctx context.Context, client stellar.StellarAPI, sequence uint64) ([]stellar.Payment, error) {
	cursor := ""
	payments := make([]stellar.Payment, 0, stellarPaymentsPageLimit)

	for {
		prevCursor := cursor
		page, err := client.GetPaymentsByLedger(ctx, sequence, cursor, stellarPaymentsPageLimit)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.Embedded.Records) == 0 {
			break
		}

		for _, payment := range page.Embedded.Records {
			payments = append(payments, payment)
			cursor = strings.TrimSpace(payment.PagingToken)
		}
		if cursor == "" || cursor == prevCursor {
			break
		}
	}

	return payments, nil
}

func classifyStellarError(err error) ErrorType {
	if err == nil {
		return ErrorTypeUnknown
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "404"), strings.Contains(msg, "not found"):
		return ErrorTypeBlockNotFound
	case strings.Contains(msg, "timeout"):
		return ErrorTypeTimeout
	default:
		return ErrorTypeUnknown
	}
}

func isSupportedStellarPayment(paymentType string) bool {
	switch strings.ToLower(strings.TrimSpace(paymentType)) {
	case "payment":
		return true
	case "create_account":
		return true
	case "path_payment_strict_receive":
		return true
	case "path_payment_strict_recieve":
		return true
	case "path_payment_strict_send":
		return true
	case "account_merge":
		return true
	default:
		return false
	}
}

func isNativeStellarPayment(payment stellar.Payment) bool {
	paymentType := strings.ToLower(strings.TrimSpace(payment.Type))
	if paymentType == "create_account" || paymentType == "account_merge" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(payment.AssetType), "native")
}

func stellarTransferFields(payment stellar.Payment) (string, string, string) {
	switch strings.ToLower(strings.TrimSpace(payment.Type)) {
	case "create_account":
		from := strings.TrimSpace(payment.Funder)
		if from == "" {
			from = strings.TrimSpace(payment.SourceAccount)
		}
		to := strings.TrimSpace(payment.Account)
		if to == "" {
			to = strings.TrimSpace(payment.Into)
		}
		return from, to, strings.TrimSpace(payment.StartingBalance)
	case "account_merge":
		from := strings.TrimSpace(payment.Account)
		if from == "" {
			from = strings.TrimSpace(payment.SourceAccount)
		}
		return from, strings.TrimSpace(payment.Into), strings.TrimSpace(payment.Amount)
	default:
		return strings.TrimSpace(payment.From), strings.TrimSpace(payment.To), strings.TrimSpace(payment.Amount)
	}
}

func formatStellarAsset(issuer string, code string) string {
	issuer = normalizeStellarAddress(issuer)
	code = strings.TrimSpace(code)
	if issuer == "" || code == "" {
		return ""
	}
	return issuer + ":" + code
}

func normalizeStellarAddress(address string) string {
	return strings.TrimSpace(address)
}

func stroopsToXLM(v string) decimal.Decimal {
	if strings.TrimSpace(v) == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(strings.TrimSpace(v)).Div(decimal.NewFromInt(10_000_000))
}

func parseRFC3339Unix(v string) (uint64, error) {
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		return 0, err
	}
	return uint64(ts.Unix()), nil
}
