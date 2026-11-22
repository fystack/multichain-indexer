package indexer

import (
	"context"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/shopspring/decimal"
)

const (
	stellarPaymentsPageLimit          = 200
	stellarOperationsPageLimit        = 200
	stellarEffectsPageLimit           = 200
	stellarBurnAddress                = "stellar:burn"
	stellarClaimableBalanceStateScope = "stellar_claimable_balance"
)

type StellarIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[stellar.StellarAPI]
	kvstore     infra.KVStore
	pubkeyStore PubkeyStore
}

func NewStellarIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[stellar.StellarAPI],
	kvstore infra.KVStore,
	pubkeyStore PubkeyStore,
) *StellarIndexer {
	return &StellarIndexer{
		chainName:   chainName,
		config:      cfg,
		failover:    failover,
		kvstore:     kvstore,
		pubkeyStore: pubkeyStore,
	}
}

func (s *StellarIndexer) GetName() string                  { return strings.ToUpper(s.chainName) }
func (s *StellarIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeStellar }
func (s *StellarIndexer) GetNetworkInternalCode() string   { return s.config.InternalCode }
func (s *StellarIndexer) NormalizeForDirection(tx types.Transaction, direction string) types.Transaction {
	return normalizeDirectionalMetadata(tx, direction)
}

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
		ledger     *stellar.Ledger
		payments   []stellar.Payment
		operations []stellar.Operation
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

		allOperations, err := fetchStellarLedgerOperations(ctx, client, number)
		if err != nil {
			return err
		}
		operations = allOperations
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get stellar ledger %d failed: %w", number, err)
	}
	if ledger == nil {
		return nil, fmt.Errorf("stellar ledger %d not found", number)
	}
	return s.convertLedger(ctx, ledger, payments, operations)
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

func (s *StellarIndexer) convertLedger(
	ctx context.Context,
	ledger *stellar.Ledger,
	payments []stellar.Payment,
	operations []stellar.Operation,
) (*types.Block, error) {
	if ledger == nil {
		return nil, fmt.Errorf("stellar ledger is nil")
	}

	timestamp, err := parseRFC3339Unix(ledger.ClosedAt)
	if err != nil {
		return nil, fmt.Errorf("parse stellar ledger time: %w", err)
	}

	txs := make([]types.Transaction, 0, len(payments)+len(operations))
	blockHash := strings.TrimSpace(ledger.Hash)
	txDetails := make(map[string]*stellar.Transaction)
	txDetailsFetched := make(map[string]bool)
	effectsByOperation := make(map[string][]stellar.Effect)
	effectsFetched := make(map[string]bool)
	for paymentIndex, payment := range payments {
		if !s.shouldProcessPayment(payment) {
			continue
		}
		txDetail, err := s.getCachedTransactionDetail(ctx, txDetails, txDetailsFetched, payment.TransactionHash)
		if err != nil {
			return nil, err
		}
		converted, ok := s.convertPayment(
			payment,
			txDetail,
			ledger.Sequence,
			blockHash,
			stellarTransferIndex(payment, paymentIndex),
			timestamp,
		)
		if !ok {
			continue
		}
		txs = append(txs, converted)
	}
	for operationIndex, operation := range operations {
		if !s.shouldProcessOperation(operation) {
			continue
		}

		var effects []stellar.Effect
		if stellarOperationNeedsEffects(operation.Type) {
			effects, err = s.getCachedOperationEffects(ctx, effectsByOperation, effectsFetched, operation.ID)
			if err != nil {
				return nil, err
			}
		}

		txDetail, err := s.getCachedTransactionDetail(ctx, txDetails, txDetailsFetched, operation.TransactionHash)
		if err != nil {
			return nil, err
		}
		converted, ok, err := s.convertOperation(
			operation,
			effects,
			txDetail,
			ledger.Sequence,
			blockHash,
			stellarOperationTransferIndex(operation, operationIndex),
			timestamp,
		)
		if err != nil {
			return nil, err
		}
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

func (s *StellarIndexer) shouldProcessPayment(payment stellar.Payment) bool {
	if !payment.TransactionSuccessful {
		return false
	}
	if !isSupportedStellarPayment(payment.Type) {
		return false
	}

	from, to, amount := stellarTransferFields(payment)
	if from == "" || to == "" || amount == "" {
		return false
	}
	if !s.isMonitoredTransfer(from, to) {
		return false
	}
	if isNativeStellarPayment(payment) {
		return true
	}
	return formatStellarAsset(payment.AssetIssuer, payment.AssetCode) != ""
}

func (s *StellarIndexer) shouldProcessOperation(operation stellar.Operation) bool {
	if !operation.TransactionSuccessful {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(operation.Type)) {
	case "clawback":
		from := normalizeStellarAddress(operation.From)
		if from == "" || !s.isMonitoredAddress(from) {
			return false
		}
		txType, _, ok := stellarAssetFromOperation(operation.Asset)
		return ok && txType == constant.TxTypeTokenTransfer && strings.TrimSpace(operation.Amount) != ""
	case "create_claimable_balance", "claim_claimable_balance", "clawback_claimable_balance":
		return true
	default:
		return false
	}
}

func stellarOperationNeedsEffects(operationType string) bool {
	switch strings.ToLower(strings.TrimSpace(operationType)) {
	case "create_claimable_balance", "claim_claimable_balance":
		return true
	default:
		return false
	}
}

func (s *StellarIndexer) getCachedTransactionDetail(
	ctx context.Context,
	cache map[string]*stellar.Transaction,
	fetched map[string]bool,
	hash string,
) (*stellar.Transaction, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil, nil
	}
	if fetched[hash] {
		return cache[hash], nil
	}

	var txDetail *stellar.Transaction
	err := s.failover.ExecuteWithRetry(ctx, func(client stellar.StellarAPI) error {
		tx, err := client.GetTransaction(ctx, hash)
		txDetail = tx
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get stellar transaction %s failed: %w", hash, err)
	}

	cache[hash] = txDetail
	fetched[hash] = true
	return txDetail, nil
}

func (s *StellarIndexer) getCachedOperationEffects(
	ctx context.Context,
	cache map[string][]stellar.Effect,
	fetched map[string]bool,
	operationID string,
) ([]stellar.Effect, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, nil
	}
	if fetched[operationID] {
		return cache[operationID], nil
	}

	var effects []stellar.Effect
	err := s.failover.ExecuteWithRetry(ctx, func(client stellar.StellarAPI) error {
		var err error
		effects, err = fetchStellarOperationEffects(ctx, client, operationID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get stellar effects for operation %s failed: %w", operationID, err)
	}

	cache[operationID] = effects
	fetched[operationID] = true
	return effects, nil
}

func (s *StellarIndexer) convertPayment(
	payment stellar.Payment,
	txDetail *stellar.Transaction,
	ledgerSequence uint64,
	blockHash string,
	transferIndex string,
	ledgerTimestamp uint64,
) (types.Transaction, bool) {
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
	var memoType types.MemoType
	if txDetail != nil {
		fee = stroopsToXLM(strings.TrimSpace(txDetail.FeeCharged))
		memo, memoType = stellarMemo(txDetail)
	}
	// A payment to a muxed (M...) address carries the recipient's id in the address instead of a memo.
	if muxedID := stellarDestinationMuxedID(payment); muxedID != "" && memo == "" {
		memo, memoType = muxedID, types.MemoTypeID
	}

	tx := types.Transaction{
		TxHash:        strings.TrimSpace(payment.TransactionHash),
		NetworkId:     s.config.NetworkId,
		BlockNumber:   ledgerSequence,
		BlockHash:     blockHash,
		TransferIndex: transferIndex,
		FromAddress:   normalizeStellarAddress(from),
		ToAddress:     normalizeStellarAddress(to),
		AssetAddress:  assetAddress,
		Amount:        amount,
		Type:          txType,
		TxFee:         fee,
		Timestamp:     timestamp,
		Confirmations: 1,
		Status:        types.StatusConfirmed,
	}
	tx.Memo = memo
	tx.MemoType = memoType
	if sourceType, sourceAssetAddress, sourceAmount, ok := stellarSourcePaymentDetails(payment); ok {
		tx.SetMetadata(metadataKeySourceTxType, string(sourceType))
		tx.SetMetadata(metadataKeySourceAmount, sourceAmount)
		if sourceType == constant.TxTypeTokenTransfer {
			tx.SetMetadata(metadataKeySourceAsset, sourceAssetAddress)
		}
	}

	return tx, true
}

type stellarClaimableBalanceState struct {
	Asset         string   `json:"asset"`
	Amount        string   `json:"amount"`
	SourceAccount string   `json:"source_account"`
	Claimants     []string `json:"claimants"`
}

func (s *StellarIndexer) convertOperation(
	operation stellar.Operation,
	effects []stellar.Effect,
	txDetail *stellar.Transaction,
	ledgerSequence uint64,
	blockHash string,
	transferIndex string,
	ledgerTimestamp uint64,
) (types.Transaction, bool, error) {
	if !operation.TransactionSuccessful {
		return types.Transaction{}, false, nil
	}

	switch strings.ToLower(strings.TrimSpace(operation.Type)) {
	case "clawback":
		tx, ok := s.convertClawbackOperation(operation, txDetail, ledgerSequence, blockHash, transferIndex, ledgerTimestamp)
		return tx, ok, nil
	case "create_claimable_balance":
		return s.convertCreateClaimableBalanceOperation(operation, effects, txDetail, ledgerSequence, blockHash, transferIndex, ledgerTimestamp)
	case "claim_claimable_balance":
		return s.convertClaimClaimableBalanceOperation(operation, effects, txDetail, ledgerSequence, blockHash, transferIndex, ledgerTimestamp)
	case "clawback_claimable_balance":
		return s.convertClawbackClaimableBalanceOperation(operation)
	default:
		return types.Transaction{}, false, nil
	}
}

func (s *StellarIndexer) convertClawbackOperation(
	operation stellar.Operation,
	txDetail *stellar.Transaction,
	ledgerSequence uint64,
	blockHash string,
	transferIndex string,
	ledgerTimestamp uint64,
) (types.Transaction, bool) {
	from := normalizeStellarAddress(operation.From)
	if from == "" || !s.isMonitoredAddress(from) {
		return types.Transaction{}, false
	}

	txType, assetAddress, ok := stellarAssetFromOperation(operation.Asset)
	if !ok || txType != constant.TxTypeTokenTransfer || strings.TrimSpace(operation.Amount) == "" {
		return types.Transaction{}, false
	}

	tx := s.newOperationTransaction(operation, txDetail, ledgerSequence, blockHash, transferIndex, ledgerTimestamp)
	tx.FromAddress = from
	tx.ToAddress = stellarBurnAddress
	tx.AssetAddress = assetAddress
	tx.Amount = strings.TrimSpace(operation.Amount)
	tx.Type = txType
	tx.SetMetadata(types.MetadataKeySubtype, "clawback")
	return tx, true
}

func (s *StellarIndexer) convertCreateClaimableBalanceOperation(
	operation stellar.Operation,
	effects []stellar.Effect,
	txDetail *stellar.Transaction,
	ledgerSequence uint64,
	blockHash string,
	transferIndex string,
	ledgerTimestamp uint64,
) (types.Transaction, bool, error) {
	if s.kvstore == nil {
		return types.Transaction{}, false, fmt.Errorf("stellar claimable balance state requires kvstore")
	}

	effect := findStellarEffect(effects, "claimable_balance_created")
	balanceID := strings.TrimSpace(operation.BalanceID)
	if balanceID == "" && effect != nil {
		balanceID = strings.TrimSpace(effect.BalanceID)
	}
	if balanceID == "" {
		return types.Transaction{}, false, nil
	}

	asset := strings.TrimSpace(operation.Asset)
	if asset == "" && effect != nil {
		asset = strings.TrimSpace(effect.Asset)
	}
	amount := strings.TrimSpace(operation.Amount)
	if amount == "" && effect != nil {
		amount = strings.TrimSpace(effect.Amount)
	}
	txType, assetAddress, ok := stellarAssetFromOperation(asset)
	if !ok || amount == "" {
		return types.Transaction{}, false, nil
	}

	sourceAccount := normalizeStellarAddress(operation.SourceAccount)
	claimants := stellarClaimantAddresses(operation.Claimants)
	state := stellarClaimableBalanceState{
		Asset:         asset,
		Amount:        amount,
		SourceAccount: sourceAccount,
		Claimants:     claimants,
	}
	if err := s.saveClaimableBalanceState(balanceID, state); err != nil {
		return types.Transaction{}, false, err
	}

	shouldEmit := s.config.TwoWayIndexing && s.isMonitoredAddress(sourceAccount)
	if !shouldEmit {
		return types.Transaction{}, false, nil
	}

	tx := s.newOperationTransaction(operation, txDetail, ledgerSequence, blockHash, transferIndex, ledgerTimestamp)
	tx.FromAddress = sourceAccount
	tx.AssetAddress = assetAddress
	tx.Amount = amount
	tx.Type = txType
	tx.SetMetadata(types.MetadataKeySubtype, "create_claimable_balance")
	tx.SetMetadata(types.MetadataKeyClaimableID, balanceID)
	if len(claimants) > 0 {
		tx.SetMetadata(types.MetadataKeyClaimants, claimants)
	}
	return tx, true, nil
}

func (s *StellarIndexer) convertClaimClaimableBalanceOperation(
	operation stellar.Operation,
	effects []stellar.Effect,
	txDetail *stellar.Transaction,
	ledgerSequence uint64,
	blockHash string,
	transferIndex string,
	ledgerTimestamp uint64,
) (types.Transaction, bool, error) {
	balanceID := strings.TrimSpace(operation.BalanceID)
	if balanceID == "" {
		return types.Transaction{}, false, nil
	}
	state, found, err := s.loadClaimableBalanceState(balanceID)
	if err != nil {
		return types.Transaction{}, false, err
	}
	if found {
		if err := s.deleteClaimableBalanceState(balanceID); err != nil {
			return types.Transaction{}, false, err
		}
	} else {
		state, found = stellarClaimableBalanceStateFromEffect(findStellarEffect(effects, "claimable_balance_claimed"))
		if !found {
			return types.Transaction{}, false, nil
		}
	}

	claimant := normalizeStellarAddress(operation.Claimant)
	if claimant == "" || !s.isMonitoredAddress(claimant) {
		return types.Transaction{}, false, nil
	}

	txType, assetAddress, ok := stellarAssetFromOperation(state.Asset)
	if !ok || strings.TrimSpace(state.Amount) == "" {
		return types.Transaction{}, false, nil
	}

	tx := s.newOperationTransaction(operation, txDetail, ledgerSequence, blockHash, transferIndex, ledgerTimestamp)
	tx.ToAddress = claimant
	tx.AssetAddress = assetAddress
	tx.Amount = strings.TrimSpace(state.Amount)
	tx.Type = txType
	tx.SetMetadata(types.MetadataKeySubtype, "claim_claimable_balance")
	tx.SetMetadata(types.MetadataKeyClaimableID, balanceID)
	return tx, true, nil
}

func (s *StellarIndexer) convertClawbackClaimableBalanceOperation(
	operation stellar.Operation) (types.Transaction, bool, error) {
	balanceID := strings.TrimSpace(operation.BalanceID)
	if balanceID == "" {
		return types.Transaction{}, false, nil
	}
	_, found, err := s.loadClaimableBalanceState(balanceID)
	if err != nil {
		return types.Transaction{}, false, err
	}
	if found {
		if err := s.deleteClaimableBalanceState(balanceID); err != nil {
			return types.Transaction{}, false, err
		}
	}
	return types.Transaction{}, false, nil
}

func (s *StellarIndexer) newOperationTransaction(
	operation stellar.Operation,
	txDetail *stellar.Transaction,
	ledgerSequence uint64,
	blockHash string,
	transferIndex string,
	ledgerTimestamp uint64,
) types.Transaction {
	timestamp := ledgerTimestamp
	if txDetail != nil && strings.TrimSpace(txDetail.CreatedAt) != "" {
		parsed, err := parseRFC3339Unix(txDetail.CreatedAt)
		if err == nil {
			timestamp = parsed
		}
	} else if strings.TrimSpace(operation.CreatedAt) != "" {
		parsed, err := parseRFC3339Unix(operation.CreatedAt)
		if err == nil {
			timestamp = parsed
		}
	}

	fee := decimal.Zero
	if txDetail != nil {
		fee = stroopsToXLM(strings.TrimSpace(txDetail.FeeCharged))
	}

	tx := types.Transaction{
		TxHash:        strings.TrimSpace(operation.TransactionHash),
		NetworkId:     s.config.NetworkId,
		BlockNumber:   ledgerSequence,
		BlockHash:     blockHash,
		TransferIndex: transferIndex,
		TxFee:         fee,
		Timestamp:     timestamp,
		Confirmations: 1,
		Status:        types.StatusConfirmed,
	}
	if txDetail != nil {
		memo, memoType := stellarMemo(txDetail)
		if memo != "" {
			tx.Memo = memo
		}
		if memoType != "" {
			tx.MemoType = memoType
		}
	}
	return tx
}

// stellarMemo returns hash and return memos as hex, the form users enter them in, instead of Horizon's base64.
// A text memo is returned exactly as sent: trimming it could turn it into another customer's memo.
func stellarMemo(txDetail *stellar.Transaction) (string, types.MemoType) {
	memo := txDetail.Memo
	memoType := types.MemoType(strings.TrimSpace(txDetail.MemoType))
	if memoType != types.MemoTypeHash && memoType != types.MemoTypeReturn {
		return memo, memoType
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(memo))
	if err != nil {
		return memo, memoType
	}
	return hex.EncodeToString(raw), memoType
}

// stellarDestinationMuxedID returns the id embedded in the muxed address a payment was sent to, or "".
func stellarDestinationMuxedID(payment stellar.Payment) string {
	if strings.EqualFold(strings.TrimSpace(payment.Type), "account_merge") {
		return strings.TrimSpace(payment.IntoMuxedID)
	}
	return strings.TrimSpace(payment.ToMuxedID)
}

func (s *StellarIndexer) saveClaimableBalanceState(balanceID string, state stellarClaimableBalanceState) error {
	if s.kvstore == nil {
		return fmt.Errorf("stellar claimable balance state requires kvstore")
	}
	return s.kvstore.SetAny(s.claimableBalanceStateKey(balanceID), state)
}

func (s *StellarIndexer) loadClaimableBalanceState(balanceID string) (stellarClaimableBalanceState, bool, error) {
	if s.kvstore == nil {
		return stellarClaimableBalanceState{}, false, fmt.Errorf("stellar claimable balance state requires kvstore")
	}
	var state stellarClaimableBalanceState
	found, err := s.kvstore.GetAny(s.claimableBalanceStateKey(balanceID), &state)
	return state, found, err
}

func (s *StellarIndexer) deleteClaimableBalanceState(balanceID string) error {
	if s.kvstore == nil {
		return fmt.Errorf("stellar claimable balance state requires kvstore")
	}
	return s.kvstore.Delete(s.claimableBalanceStateKey(balanceID))
}

func (s *StellarIndexer) claimableBalanceStateKey(balanceID string) string {
	return fmt.Sprintf("%s/%s/%s", stellarClaimableBalanceStateScope, strings.TrimSpace(s.config.NetworkId), strings.TrimSpace(balanceID))
}

func (s *StellarIndexer) isMonitoredAddress(address string) bool {
	if s.pubkeyStore == nil {
		return true
	}
	address = normalizeStellarAddress(address)
	return address != "" && s.pubkeyStore.Exist(enum.NetworkTypeStellar, address)
}

func stellarAssetFromOperation(asset string) (constant.TxType, string, bool) {
	asset = strings.TrimSpace(asset)
	if asset == "" {
		return "", "", false
	}
	if strings.EqualFold(asset, "native") {
		return constant.TxTypeNativeTransfer, "", true
	}
	parts := strings.SplitN(asset, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	assetAddress := formatStellarAsset(parts[1], parts[0])
	if assetAddress == "" {
		return "", "", false
	}
	return constant.TxTypeTokenTransfer, assetAddress, true
}

func stellarClaimantAddresses(claimants []stellar.Claimant) []string {
	addresses := make([]string, 0, len(claimants))
	for _, claimant := range claimants {
		address := normalizeStellarAddress(claimant.Destination)
		if address == "" {
			continue
		}
		addresses = append(addresses, address)
	}
	return addresses
}

func findStellarEffect(effects []stellar.Effect, effectType string) *stellar.Effect {
	for i := range effects {
		if strings.EqualFold(strings.TrimSpace(effects[i].Type), effectType) {
			return &effects[i]
		}
	}
	return nil
}

func stellarClaimableBalanceStateFromEffect(effect *stellar.Effect) (stellarClaimableBalanceState, bool) {
	if effect == nil {
		return stellarClaimableBalanceState{}, false
	}

	state := stellarClaimableBalanceState{
		Asset:  strings.TrimSpace(effect.Asset),
		Amount: strings.TrimSpace(effect.Amount),
	}
	if claimant := normalizeStellarAddress(effect.Account); claimant != "" {
		state.Claimants = []string{claimant}
	}
	if state.Asset == "" || state.Amount == "" {
		return stellarClaimableBalanceState{}, false
	}
	return state, true
}

func stellarTransferIndex(payment stellar.Payment, paymentIndex int) string {
	if pagingToken := strings.TrimSpace(payment.PagingToken); pagingToken != "" {
		return pagingToken
	}
	if id := strings.TrimSpace(payment.ID); id != "" {
		return id
	}
	return fmt.Sprintf("%d", paymentIndex)
}

func stellarOperationTransferIndex(operation stellar.Operation, operationIndex int) string {
	if pagingToken := strings.TrimSpace(operation.PagingToken); pagingToken != "" {
		return pagingToken
	}
	if id := strings.TrimSpace(operation.ID); id != "" {
		return id
	}
	return fmt.Sprintf("%d", operationIndex)
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

func fetchStellarLedgerOperations(ctx context.Context, client stellar.StellarAPI, sequence uint64) ([]stellar.Operation, error) {
	cursor := ""
	operations := make([]stellar.Operation, 0, stellarOperationsPageLimit)

	for {
		prevCursor := cursor
		page, err := client.GetOperationsByLedger(ctx, sequence, cursor, stellarOperationsPageLimit)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.Embedded.Records) == 0 {
			break
		}

		for _, operation := range page.Embedded.Records {
			operations = append(operations, operation)
			cursor = strings.TrimSpace(operation.PagingToken)
		}
		if cursor == "" || cursor == prevCursor {
			break
		}
	}

	return operations, nil
}

func fetchStellarOperationEffects(ctx context.Context, client stellar.StellarAPI, operationID string) ([]stellar.Effect, error) {
	cursor := ""
	effects := make([]stellar.Effect, 0, stellarEffectsPageLimit)

	for {
		prevCursor := cursor
		page, err := client.GetEffectsByOperation(ctx, operationID, cursor, stellarEffectsPageLimit)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.Embedded.Records) == 0 {
			break
		}

		for _, effect := range page.Embedded.Records {
			effects = append(effects, effect)
			cursor = strings.TrimSpace(effect.PagingToken)
		}
		if cursor == "" || cursor == prevCursor {
			break
		}
	}

	return effects, nil
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

func stellarSourcePaymentDetails(payment stellar.Payment) (constant.TxType, string, string, bool) {
	switch strings.ToLower(strings.TrimSpace(payment.Type)) {
	case "path_payment_strict_receive", "path_payment_strict_recieve", "path_payment_strict_send":
	default:
		return "", "", "", false
	}

	amount := strings.TrimSpace(payment.SourceAmount)
	if amount == "" {
		return "", "", "", false
	}
	if strings.EqualFold(strings.TrimSpace(payment.SourceAssetType), "native") {
		return constant.TxTypeNativeTransfer, "", amount, true
	}

	assetAddress := formatStellarAsset(payment.SourceAssetIssuer, payment.SourceAssetCode)
	if assetAddress == "" {
		return "", "", "", false
	}
	return constant.TxTypeTokenTransfer, assetAddress, amount, true
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
