package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/aptos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
)

type AptosIndexer struct {
	chainName string
	config    config.ChainConfig
	failover  *rpc.Failover[aptos.AptosAPI]
}

func NewAptosIndexer(chainName string, cfg config.ChainConfig, f *rpc.Failover[aptos.AptosAPI]) *AptosIndexer {
	return &AptosIndexer{
		chainName: chainName,
		config:    cfg,
		failover:  f,
	}
}

func (a *AptosIndexer) GetName() string                  { return strings.ToUpper(a.chainName) }
func (a *AptosIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeApt }
func (a *AptosIndexer) GetNetworkInternalCode() string   { return a.config.InternalCode }
func (a *AptosIndexer) GetNetworkId() string             { return a.config.NetworkId }

func (a *AptosIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
		info, err := c.GetLedgerInfo(ctx)
		if err != nil {
			return err
		}
		blockHeight, err := aptos.ParseVersion(info.BlockHeight)
		if err != nil {
			return err
		}
		latest = blockHeight
		return nil
	})
	return latest, err
}

func (a *AptosIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	var aptosBlock *aptos.Block

	err := a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
		block, err := c.GetBlockByHeight(ctx, blockNumber, false)
		if err != nil {
			return err
		}
		aptosBlock = block
		return nil
	})

	if err != nil {
		return nil, err
	}

	return a.processBlockWithTransactions(ctx, aptosBlock)
}

// processBlockWithTransactions fetches and processes transactions for a block
func (a *AptosIndexer) processBlockWithTransactions(ctx context.Context, aptosBlock *aptos.Block) (*types.Block, error) {
	firstVersion, err := aptos.ParseVersion(aptosBlock.FirstVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid first_version: %w", err)
	}

	lastVersion, err := aptos.ParseVersion(aptosBlock.LastVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid last_version: %w", err)
	}

	if lastVersion < firstVersion {
		return nil, fmt.Errorf("invalid version range: last_version %d < first_version %d", lastVersion, firstVersion)
	}

	totalTxs := lastVersion - firstVersion + 1
	txBatchSize := uint64(100)

	var allTxs []aptos.Transaction

	if totalTxs <= txBatchSize {
		var txs []aptos.Transaction
		err := a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
			batch, err := c.GetTransactionsByVersion(ctx, firstVersion, totalTxs)
			if err != nil {
				return err
			}
			txs = batch
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to fetch transactions: %w", err)
		}
		allTxs = txs
	} else {
		currentVersion := firstVersion
		for currentVersion <= lastVersion {
			limit := txBatchSize
			remaining := lastVersion - currentVersion + 1
			if remaining < limit {
				limit = remaining
			}

			var txs []aptos.Transaction
			err := a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
				batch, err := c.GetTransactionsByVersion(ctx, currentVersion, limit)
				if err != nil {
					return err
				}
				txs = batch
				return nil
			})

			if err != nil {
				return nil, fmt.Errorf("failed to fetch transactions at version %d: %w", currentVersion, err)
			}

			allTxs = append(allTxs, txs...)
			currentVersion += limit
		}
	}

	return a.processBlock(aptosBlock, allTxs)
}

func (a *AptosIndexer) processBlock(
	aptosBlock *aptos.Block,
	txs []aptos.Transaction,
) (*types.Block, error) {
	blockHeight, err := aptos.ParseVersion(aptosBlock.BlockHeight)
	if err != nil {
		return nil, fmt.Errorf("invalid block_height: %w", err)
	}

	timestamp, err := aptos.ParseVersion(aptosBlock.BlockTimestamp)
	if err != nil {
		return nil, fmt.Errorf("invalid block_timestamp: %w", err)
	}

	block := &types.Block{
		Number:       blockHeight,
		Hash:         aptosBlock.BlockHash,
		ParentHash:   "",
		Timestamp:    timestamp / 1000000,
		Transactions: []types.Transaction{},
	}

	for _, tx := range txs {
		transfers := tx.ExtractTransfers(a.GetNetworkId(), blockHeight)
		block.Transactions = append(block.Transactions, transfers...)
	}

	block.Transactions = utils.DedupTransfers(block.Transactions)
	return block, nil
}

func (a *AptosIndexer) GetBlocks(
	ctx context.Context,
	start, end uint64,
	isParallel bool,
) ([]BlockResult, error) {
	var nums []uint64
	for i := start; i <= end; i++ {
		nums = append(nums, i)
	}
	return a.GetBlocksByNumbers(ctx, nums)
}

func (a *AptosIndexer) GetBlocksByNumbers(
	ctx context.Context,
	nums []uint64,
) ([]BlockResult, error) {
	return a.getBlocksOptimized(ctx, nums)
}

// getBlocksOptimized fetches blocks with optimized parallel processing:
// 1. Fetches all block metadata in parallel (small, fast)
// 2. Processes blocks with transactions in parallel
func (a *AptosIndexer) getBlocksOptimized(ctx context.Context, nums []uint64) ([]BlockResult, error) {
	if len(nums) == 0 {
		return nil, nil
	}

	results := make([]BlockResult, len(nums))

	aptosBlocks := make([]*aptos.Block, len(nums))

	type metaJob struct {
		num   uint64
		index int
	}

	metaWorkers := min(len(nums), a.config.Throttle.Concurrency*2)
	metaJobs := make(chan metaJob, metaWorkers*2)
	var metaWg sync.WaitGroup

	for i := 0; i < metaWorkers; i++ {
		metaWg.Add(1)
		go func() {
			defer metaWg.Done()
			for j := range metaJobs {
				var block *aptos.Block
				err := a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
					b, err := c.GetBlockByHeight(ctx, j.num, false)
					if err != nil {
						return err
					}
					block = b
					return nil
				})

				if err != nil {
					results[j.index] = BlockResult{
						Number: j.num,
						Error:  &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
					}
				} else {
					aptosBlocks[j.index] = block
				}
			}
		}()
	}

	go func() {
		defer close(metaJobs)
		for i, num := range nums {
			select {
			case <-ctx.Done():
				return
			case metaJobs <- metaJob{num: num, index: i}:
			}
		}
	}()

	metaWg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var firstErr error
	for _, r := range results {
		if r.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", r.Number, r.Error.Message)
			return results, firstErr
		}
	}

	type txJob struct {
		aptosBlock *aptos.Block
		index      int
	}

	txWorkers := min(len(nums), a.config.Throttle.Concurrency)
	txJobs := make(chan txJob, txWorkers*2)
	var txWg sync.WaitGroup

	for i := 0; i < txWorkers; i++ {
		txWg.Add(1)
		go func() {
			defer txWg.Done()
			for j := range txJobs {
				blk, err := a.processBlockWithTransactions(ctx, j.aptosBlock)
				blockHeight, _ := aptos.ParseVersion(j.aptosBlock.BlockHeight)
				results[j.index] = BlockResult{
					Number: blockHeight,
					Block:  blk,
				}
				if err != nil {
					results[j.index].Error = &Error{
						ErrorType: ErrorTypeUnknown,
						Message:   err.Error(),
					}
				}
			}
		}()
	}

	go func() {
		defer close(txJobs)
		for i, block := range aptosBlocks {
			if block == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case txJobs <- txJob{aptosBlock: block, index: i}:
			}
		}
	}()

	txWg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	for _, r := range results {
		if r.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", r.Number, r.Error.Message)
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
