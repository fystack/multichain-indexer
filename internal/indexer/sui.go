package indexer

import (
	"context"
	"fmt"
	"math/big"
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
	// Limit concurrency to avoid overwhelming the node
	// Default to 10 concurrent requests
	const maxConcurrency = 10
	sem := semaphore.NewWeighted(maxConcurrency)

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

	// Convert map to slice in order of requested blockNumbers
	results := make([]BlockResult, 0, len(blockNumbers))
	for _, num := range blockNumbers {
		if res, ok := resultsMap[num]; ok {
			results = append(results, res)
		}
	}

	return results, nil
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
		tx := s.convertTransaction(execTx, cp.SequenceNumber(), ts)
		if tx.ToAddress == "" {
			continue
		}
		if s.pubkeyStore != nil && !s.pubkeyStore.Exist(enum.NetworkTypeSui, tx.ToAddress) {
			continue
		}
		txs = append(txs, tx)
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
	t := types.Transaction{
		TxHash:      execTx.GetDigest(),
		NetworkId:   s.cfg.NetworkId,
		BlockNumber: blockNumber,
		Timestamp:   blockTs,
	}

	// 1. Sender
	if execTx.Transaction != nil {
		t.FromAddress = execTx.Transaction.GetSender()
	}

	// 2. TxFee
	// Cost = StorageCost + ComputationCost + NonRefundableStorageFee - StorageRebate
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

	// 3. Amount and ToAddress
	// This is a heuristic: find the largest positive balance change to a non-sender
	// Note: Sui PTBs can have multiple transfers. This maps to a single "main" action.
	var maxAmount uint64
	var maxAsset string
	var receiver string

	for _, bc := range execTx.BalanceChanges {
		if bc.GetAddress() == t.FromAddress {
			continue
		}

		// Amount is string in proto, need to parse
		amtStr := bc.GetAmount()
		amtInt, ok := new(big.Int).SetString(amtStr, 10)
		// Only look for positive amounts (deposits)
		if ok && amtInt.Sign() > 0 {
			val := amtInt.Uint64()
			if val > maxAmount {
				maxAmount = val
				maxAsset = bc.GetCoinType()
				receiver = bc.GetAddress()
			}
		}
	}

	if receiver != "" {
		t.ToAddress = receiver
		amount := decimal.NewFromBigInt(new(big.Int).SetUint64(maxAmount), 0)
		t.AssetAddress = maxAsset
		if maxAsset == suiNativeCoinType {
			amount = amount.Div(decimal.NewFromInt(suiMistPerSUI))
			t.Type = constant.TxTypeNativeTransfer
		} else {
			t.Type = constant.TxTypeTokenTransfer
		}
		t.Amount = amount.String()
	}

	return t
}
