package indexer

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/sui"
	v2 "github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/semaphore"
)

// SuiIndexer implements the generic Indexer interface for the Sui blockchain,
// using the gRPC-based SuiAPI client defined in internal/rpc/sui.
type SuiIndexer struct {
	chainName   string
	cfg         config.ChainConfig
	failover    *rpc.Failover[sui.SuiAPI]
	pubkeyStore PubkeyStore
}

const suiMistPerSUI = 1_000_000_000
const suiNativeCoinType = "0x2::sui::SUI"
const suiSystemPackage = "0x3"
const suiZeroAddress = "0x0"
const suiMoveCallStake = "stake"
const suiMoveCallUnstake = "unstake"
const suiMoveCallSwap = "swap"

type suiBalanceDelta struct {
	Address  string
	CoinType string
	Amount   *big.Int
}

func isSuiNativeCoinType(coinType string) bool {
	coinType = strings.TrimSpace(coinType)
	if coinType == "" {
		return false
	}

	// Some nodes/serializers may return wrapped types (e.g. Coin<0x2::sui::SUI>).
	if i := strings.Index(coinType, "<"); i >= 0 && strings.HasSuffix(coinType, ">") {
		inner := strings.TrimSpace(coinType[i+1 : len(coinType)-1])
		if isSuiNativeCoinType(inner) {
			return true
		}
	}

	parts := strings.Split(coinType, "::")
	if len(parts) != 3 {
		return false
	}

	return normalizeSuiAddress(parts[0]) == "0x2" &&
		strings.EqualFold(parts[1], "sui") &&
		strings.EqualFold(parts[2], "SUI")
}

func normalizeSuiAddress(addr string) string {
	addr = strings.TrimSpace(strings.ToLower(addr))
	if !strings.HasPrefix(addr, "0x") {
		return addr
	}

	hex := strings.TrimLeft(addr[2:], "0")
	if hex == "" {
		hex = "0"
	}
	return "0x" + hex
}

func normalizeSuiIdentifier(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func sameSuiAddress(a, b string) bool {
	return normalizeSuiAddress(a) == normalizeSuiAddress(b)
}

func isSuiSystemSender(addr string) bool {
	return sameSuiAddress(addr, suiZeroAddress)
}

func makeSuiBaseTransaction(execTx *v2.ExecutedTransaction, networkID string, blockNumber, blockTs uint64) types.Transaction {
	t := types.Transaction{
		TxHash:      execTx.GetDigest(),
		NetworkId:   networkID,
		BlockNumber: blockNumber,
		Timestamp:   blockTs,
	}

	if execTx.Transaction != nil {
		t.FromAddress = execTx.Transaction.GetSender()
	}

	if execTx.Effects != nil && execTx.Effects.GasUsed != nil {
		g := execTx.Effects.GasUsed
		cost := uint64(0)
		if g.StorageCost != nil {
			cost += *g.StorageCost
		}
		if g.ComputationCost != nil {
			cost += *g.ComputationCost
		}
		if g.NonRefundableStorageFee != nil {
			cost += *g.NonRefundableStorageFee
		}
		if g.StorageRebate != nil {
			if cost > *g.StorageRebate {
				cost -= *g.StorageRebate
			} else {
				cost = 0
			}
		}
		t.TxFee = decimal.NewFromBigInt(new(big.Int).SetUint64(cost), 0).Div(decimal.NewFromInt(suiMistPerSUI))
	}

	return t
}

func parseSuiBalanceChanges(changes []*v2.BalanceChange) []suiBalanceDelta {
	deltas := make([]suiBalanceDelta, 0, len(changes))
	for _, bc := range changes {
		if bc == nil {
			continue
		}
		amt, ok := new(big.Int).SetString(bc.GetAmount(), 10)
		if !ok || amt.Sign() == 0 {
			continue
		}
		deltas = append(deltas, suiBalanceDelta{
			Address:  bc.GetAddress(),
			CoinType: bc.GetCoinType(),
			Amount:   amt,
		})
	}
	return deltas
}

func biggestPositiveDelta(deltas []suiBalanceDelta, skipAddr string) (suiBalanceDelta, bool) {
	var best suiBalanceDelta
	found := false
	for _, delta := range deltas {
		if skipAddr != "" && sameSuiAddress(delta.Address, skipAddr) {
			continue
		}
		if delta.Amount.Sign() <= 0 {
			continue
		}
		if !found || delta.Amount.Cmp(best.Amount) > 0 {
			best = delta
			found = true
		}
	}
	return best, found
}

func biggestNegativeDeltaByCoinType(deltas []suiBalanceDelta, coinType string) (suiBalanceDelta, bool) {
	var best suiBalanceDelta
	found := false
	for _, delta := range deltas {
		if delta.Amount.Sign() >= 0 || delta.CoinType != coinType {
			continue
		}
		if !found || delta.Amount.Cmp(best.Amount) < 0 {
			best = delta
			found = true
		}
	}
	return best, found
}

func parseBigIntString(v string) (*big.Int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	n, ok := new(big.Int).SetString(v, 10)
	return n, ok
}

func classifySuiTransferType(coinType string) (constant.TxType, string) {
	if isSuiNativeCoinType(coinType) {
		return constant.TxTypeNativeTransfer, suiNativeCoinType
	}
	return constant.TxTypeTokenTransfer, coinType
}

func normalizeSuiMovementType(tx *types.Transaction) {
	if tx == nil {
		return
	}
	tx.Type, tx.AssetAddress = classifySuiTransferType(tx.AssetAddress)
}

func eventJSONMap(evt *v2.Event) map[string]any {
	if evt == nil || evt.GetJson() == nil {
		return nil
	}
	raw := evt.GetJson().AsInterface()
	m, _ := raw.(map[string]any)
	return m
}

func jsonString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch val := v.(type) {
		case string:
			return strings.TrimSpace(val)
		case float64:
			if val == float64(int64(val)) {
				return strconv.FormatInt(int64(val), 10)
			}
		}
	}
	return ""
}

func eventAmountString(m map[string]any) string {
	for _, key := range []string{"amount", "value", "qty"} {
		if v := jsonString(m, key); v != "" {
			if n, ok := parseBigIntString(v); ok {
				return n.String()
			}
		}
	}
	return "0"
}

func eventCoinType(evt *v2.Event, m map[string]any) string {
	if coinType := jsonString(m, "coinType", "coin_type", "type", "asset", "tokenType"); coinType != "" {
		return coinType
	}
	eventType := evt.GetEventType()
	if parts := strings.Split(eventType, "::"); len(parts) >= 3 {
		return strings.Join(parts[:3], "::")
	}
	return ""
}

func isSuiSwapModule(module string) bool {
	module = normalizeSuiIdentifier(module)
	for _, keyword := range []string{"pool", "router", "amm", "clmm"} {
		if strings.Contains(module, keyword) {
			return true
		}
	}
	return false
}

func isSuiSwapFunction(function string) bool {
	function = normalizeSuiIdentifier(function)
	for _, keyword := range []string{"swap", "swap_exact", "swap_x", "swap_y", "swap_a", "swap_b"} {
		if strings.Contains(function, keyword) {
			return true
		}
	}
	return false
}

func isSuiSwapEvent(evt *v2.Event) bool {
	if evt == nil {
		return false
	}
	eventType := normalizeSuiIdentifier(evt.GetEventType())
	module := normalizeSuiIdentifier(evt.GetModule())

	switch {
	case strings.Contains(eventType, "::swapevent"),
		strings.Contains(eventType, "::swap_event"),
		strings.Contains(eventType, "::swapexecutedevent"),
		strings.Contains(eventType, "::swap_executed_event"),
		strings.Contains(eventType, "::swapcompletedevent"),
		strings.Contains(eventType, "::swap_completed_event"):
		return true
	case isSuiSwapModule(module):
		return strings.Contains(eventType, "swap")
	default:
		return false
	}
}

func isSuiValidatorStakeEvent(evt *v2.Event) bool {
	if evt == nil {
		return false
	}
	eventType := normalizeSuiIdentifier(evt.GetEventType())
	module := normalizeSuiIdentifier(evt.GetModule())
	return strings.Contains(eventType, "::validator::stakingrequestevent") ||
		(module == "validator" && strings.Contains(eventType, "stakingrequest"))
}

func isSuiValidatorUnstakeEvent(evt *v2.Event) bool {
	if evt == nil {
		return false
	}
	eventType := normalizeSuiIdentifier(evt.GetEventType())
	module := normalizeSuiIdentifier(evt.GetModule())
	return (module == "validator" || strings.Contains(eventType, "::validator::")) &&
		(strings.Contains(eventType, "withdrawstake") ||
			strings.Contains(eventType, "unstake") ||
			strings.Contains(eventType, "withdrawal"))
}

func commandSummary(tx *v2.Transaction) (moveCalls []*v2.MoveCall) {
	if tx == nil || tx.GetKind() == nil {
		return
	}
	pt := tx.GetKind().GetProgrammableTransaction()
	if pt == nil {
		return
	}
	for _, cmd := range pt.GetCommands() {
		if cmd.GetMoveCall() != nil {
			moveCalls = append(moveCalls, cmd.GetMoveCall())
		}
	}
	return
}

func classifyMoveCall(mc *v2.MoveCall) string {
	if mc == nil {
		return ""
	}
	module := normalizeSuiIdentifier(mc.GetModule())
	function := normalizeSuiIdentifier(mc.GetFunction())
	pkg := normalizeSuiAddress(mc.GetPackage())

	switch {
	case pkg == suiSystemPackage && module == "sui_system" &&
		(strings.Contains(function, "add_stake") || strings.Contains(function, "request_add_stake")):
		return suiMoveCallStake
	case pkg == suiSystemPackage && module == "sui_system" &&
		(strings.Contains(function, "withdraw_stake") || strings.Contains(function, "request_withdraw_stake") || strings.Contains(function, "unstake")):
		return suiMoveCallUnstake
	case isSuiSwapModule(module) && isSuiSwapFunction(function):
		return suiMoveCallSwap
	default:
		return ""
	}
}

func uniqueTransactions(txs []types.Transaction) []types.Transaction {
	if len(txs) <= 1 {
		return txs
	}

	out := make([]types.Transaction, 0, len(txs))
	seen := make(map[string]struct{}, len(txs))
	for _, tx := range txs {
		key := strings.Join([]string{
			tx.TxHash,
			string(tx.Type),
			normalizeSuiAddress(tx.FromAddress),
			normalizeSuiAddress(tx.ToAddress),
			tx.AssetAddress,
			tx.Amount,
			tx.TransferIndex,
		}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tx)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].AssetAddress != out[j].AssetAddress {
			return out[i].AssetAddress < out[j].AssetAddress
		}
		if out[i].ToAddress != out[j].ToAddress {
			return out[i].ToAddress < out[j].ToAddress
		}
		return out[i].Amount < out[j].Amount
	})

	return out
}

func (s *SuiIndexer) transferTransactionsFromBalanceChanges(base types.Transaction, deltas []suiBalanceDelta) []types.Transaction {
	out := make([]types.Transaction, 0, len(deltas))
	for i, delta := range deltas {
		if delta.Amount.Sign() <= 0 {
			continue
		}
		tx := base
		if senderDelta, ok := biggestNegativeDeltaByCoinType(deltas, delta.CoinType); ok {
			tx.FromAddress = senderDelta.Address
		}
		tx.ToAddress = delta.Address
		tx.Amount = delta.Amount.String()
		tx.Type, tx.AssetAddress = classifySuiTransferType(delta.CoinType)
		tx.TransferIndex = fmt.Sprintf("balance:%d", i)
		out = append(out, tx)
	}
	return out
}

func (s *SuiIndexer) eventTransactions(base types.Transaction, execTx *v2.ExecutedTransaction) []types.Transaction {
	if execTx.GetEvents() == nil {
		return nil
	}

	var out []types.Transaction
	for _, evt := range execTx.GetEvents().GetEvents() {
		if evt == nil {
			continue
		}
		eventType := normalizeSuiIdentifier(evt.GetEventType())
		data := eventJSONMap(evt)

		switch {
		case isSuiValidatorStakeEvent(evt):
			tx := base
			tx.ToAddress = jsonString(data, "validator_address", "validator", "pool_id")
			if tx.ToAddress == "" {
				tx.ToAddress = base.FromAddress
			}
			tx.AssetAddress = suiNativeCoinType
			tx.Amount = eventAmountString(data)
			normalizeSuiMovementType(&tx)
			out = append(out, tx)
		case isSuiValidatorUnstakeEvent(evt):
			tx := base
			tx.ToAddress = base.FromAddress
			tx.AssetAddress = suiNativeCoinType
			tx.Amount = eventAmountString(data)
			normalizeSuiMovementType(&tx)
			out = append(out, tx)
		case isSuiSwapEvent(evt):
			tx := base
			tx.ToAddress = base.FromAddress
			tx.AssetAddress = eventCoinType(evt, data)
			tx.Amount = eventAmountString(data)
			normalizeSuiMovementType(&tx)
			out = append(out, tx)
		case strings.Contains(eventType, "::transfer"):
			tx := base
			tx.FromAddress = jsonString(data, "from", "sender")
			if tx.FromAddress == "" {
				tx.FromAddress = base.FromAddress
			}
			tx.ToAddress = jsonString(data, "to", "recipient", "receiver", "owner")
			if tx.ToAddress == "" {
				continue
			}
			tx.AssetAddress = eventCoinType(evt, data)
			tx.Amount = eventAmountString(data)
			normalizeSuiMovementType(&tx)
			out = append(out, tx)
		}
	}

	return out
}

func NewSuiIndexer(chainName string, cfg config.ChainConfig, f *rpc.Failover[sui.SuiAPI], pubkeyStore PubkeyStore) *SuiIndexer {
	s := &SuiIndexer{
		chainName:   chainName,
		cfg:         cfg,
		failover:    f,
		pubkeyStore: pubkeyStore,
	}

	// Start streaming in background on best available node
	go func() {
		// Small delay to allow initial connection
		time.Sleep(1 * time.Second)
		_ = s.failover.ExecuteWithRetry(context.Background(), func(c sui.SuiAPI) error {
			return c.StartStreaming(context.Background())
		})
	}()

	return s
}

func (s *SuiIndexer) GetName() string                  { return strings.ToUpper(s.chainName) }
func (s *SuiIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeSui }
func (s *SuiIndexer) GetNetworkInternalCode() string   { return s.cfg.InternalCode }

func (s *SuiIndexer) isMonitoredAddress(addr string) bool {
	if s.pubkeyStore == nil {
		return true
	}

	candidates := []string{addr, normalizeSuiAddress(addr)}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if s.pubkeyStore.Exist(enum.NetworkTypeSui, candidate) {
			return true
		}
	}

	return false
}

func (s *SuiIndexer) isMonitoredTransfer(from, to string) bool {
	if s.pubkeyStore == nil {
		return true
	}

	if s.isMonitoredAddress(to) {
		return true
	}

	return s.cfg.TwoWayIndexing && s.isMonitoredAddress(from)
}

// GetLatestBlockNumber returns the latest checkpoint sequence number.
func (s *SuiIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {

	timeout := s.cfg.Client.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var latest uint64
	err := s.failover.ExecuteWithRetry(ctx, func(c sui.SuiAPI) error {
		n, err := c.GetLatestCheckpointSequence(ctx)
		latest = n
		return err
	})
	return latest, err
}

// GetBlock fetches a single checkpoint and converts it into the generic Block type.
func (s *SuiIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	var cp *sui.Checkpoint
	err := s.failover.ExecuteWithRetry(ctx, func(c sui.SuiAPI) error {
		var err error
		cp, err = c.GetCheckpoint(ctx, number)
		return err
	})

	// Optimization: If block not found but theoretically exists (based on latest height),
	// wait briefly and retry once to handle public node lag.
	if err != nil && strings.Contains(err.Error(), "not found") {
		latest, _ := s.GetLatestBlockNumber(ctx)
		if latest > 0 && number <= latest {
			time.Sleep(500 * time.Millisecond)
			_ = s.failover.ExecuteWithRetry(ctx, func(c sui.SuiAPI) error {
				var retryErr error
				cp, retryErr = c.GetCheckpoint(ctx, number)
				return retryErr
			})
			// If cp is found now, clear error
			if cp != nil {
				err = nil
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("get sui checkpoint %d failed: %w", number, err)
	}
	if cp == nil {
		return nil, fmt.Errorf("sui checkpoint %d not found", number)
	}
	return s.convertCheckpoint(cp), nil
}

// GetBlocks fetches a contiguous range of checkpoints.
func (s *SuiIndexer) GetBlocks(
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
	return s.GetBlocksByNumbers(ctx, nums)
}

// GetBlocksByNumbers fetches checkpoints by sequence numbers.
//
// Optimized with concurrent workers using a semaphore.
func (s *SuiIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}

	// Use configured concurrency when available, fallback to a safe default.
	workers := s.cfg.Throttle.Concurrency
	if workers <= 0 {
		workers = 10
	}
	if workers > 50 {
		workers = 50
	}
	sem := semaphore.NewWeighted(int64(workers))

	// Results map protected by mutex
	resultsMap := make(map[uint64]BlockResult)
	var mu sync.Mutex

	var wg sync.WaitGroup

	for _, num := range blockNumbers {
		if err := sem.Acquire(ctx, 1); err != nil {
			return nil, err
		}
		wg.Add(1)

		go func(blockNum uint64) {
			defer sem.Release(1)
			defer wg.Done()

			blk, err := s.GetBlock(ctx, blockNum)
			res := BlockResult{Number: blockNum}

			if err != nil {
				res.Error = &Error{
					ErrorType: ErrorTypeUnknown,
					Message:   err.Error(),
				}
			} else {
				res.Block = blk
			}

			mu.Lock()
			resultsMap[blockNum] = res
			mu.Unlock()
		}(num)
	}

	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Convert map to slice in order of requested blockNumbers and surface first error.
	results := make([]BlockResult, 0, len(blockNumbers))
	var firstErr error
	for _, num := range blockNumbers {
		if res, ok := resultsMap[num]; ok {
			results = append(results, res)
			if firstErr == nil && res.Error != nil {
				firstErr = fmt.Errorf("block %d: %s", res.Number, res.Error.Message)
			}
		}
	}

	return results, firstErr
}

// IsHealthy does a quick gRPC health check by asking for the latest checkpoint.
func (s *SuiIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.GetLatestBlockNumber(ctx)
	return err == nil
}

// convertCheckpoint maps a Sui checkpoint into the generic Block representation.
func (s *SuiIndexer) convertCheckpoint(cp *sui.Checkpoint) *types.Block {
	// Sui timestamps are typically in milliseconds.
	ts := cp.TimestampMs() / 1000

	txs := make([]types.Transaction, 0, len(cp.GetTransactions()))
	for _, execTx := range cp.GetTransactions() {
		for _, tx := range s.convertTransactions(execTx, cp.SequenceNumber(), ts) {
			if tx.ToAddress == "" {
				continue
			}
			if !s.isMonitoredTransfer(tx.FromAddress, tx.ToAddress) {
				continue
			}
			tx.BlockHash = cp.Digest()
			txs = append(txs, tx)
		}
	}

	return &types.Block{
		Number:       cp.SequenceNumber(),
		Hash:         cp.Digest(),
		ParentHash:   cp.PreviousDigest(),
		Timestamp:    ts,
		Transactions: txs,
	}
}

func (s *SuiIndexer) convertTransaction(execTx *v2.ExecutedTransaction, blockNumber, blockTs uint64) types.Transaction {
	txs := s.convertTransactions(execTx, blockNumber, blockTs)
	if len(txs) == 0 {
		return types.Transaction{}
	}
	return txs[0]
}

func (s *SuiIndexer) convertTransactions(execTx *v2.ExecutedTransaction, blockNumber, blockTs uint64) []types.Transaction {
	base := makeSuiBaseTransaction(execTx, s.cfg.InternalCode, blockNumber, blockTs)
	if base.FromAddress == "" || isSuiSystemSender(base.FromAddress) {
		return nil
	}

	deltas := parseSuiBalanceChanges(execTx.GetBalanceChanges())
	var out []types.Transaction

	out = append(out, s.transferTransactionsFromBalanceChanges(base, deltas)...)

	moveCalls := commandSummary(execTx.GetTransaction())

	for _, mc := range moveCalls {
		eventType := classifyMoveCall(mc)
		if eventType == "" {
			continue
		}
		tx := base
		tx.ToAddress = base.FromAddress
		tx.Amount = "0"

		switch eventType {
		case suiMoveCallSwap:
			if delta, ok := biggestPositiveDelta(deltas, ""); ok {
				tx.ToAddress = base.FromAddress
				tx.Amount = delta.Amount.String()
				tx.AssetAddress = delta.CoinType
			}
		case suiMoveCallStake, suiMoveCallUnstake:
			if delta, ok := biggestPositiveDelta(deltas, ""); ok {
				tx.AssetAddress = delta.CoinType
				tx.Amount = delta.Amount.String()
			}
		}

		normalizeSuiMovementType(&tx)
		out = append(out, tx)
	}

	out = append(out, s.eventTransactions(base, execTx)...)

	return uniqueTransactions(out)
}
