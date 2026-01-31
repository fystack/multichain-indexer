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
		version, err := aptos.ParseVersion(info.BlockHeight)
		if err != nil {
			return err
		}
		latest = version
		return nil
	})
	return latest, err
}

func (a *AptosIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	var aptosBlock *aptos.Block

	err := a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
		block, err := c.GetBlockByVersion(ctx, blockNumber, false)
		if err != nil {
			return err
		}
		aptosBlock = block
		return nil
	})

	if err != nil {
		return nil, err
	}

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

	limit := lastVersion - firstVersion + 1
	if limit > 100 {
		limit = 100
	}

	var txs []aptos.Transaction
	err = a.failover.ExecuteWithRetry(ctx, func(c aptos.AptosAPI) error {
		transactions, err := c.GetTransactionsByVersion(ctx, firstVersion, limit)
		if err != nil {
			return err
		}
		txs = transactions
		return nil
	})

	if err != nil {
		return nil, err
	}

	return a.processBlock(aptosBlock, txs)
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
	return a.getBlocks(ctx, nums)
}

func (a *AptosIndexer) getBlocks(ctx context.Context, nums []uint64) ([]BlockResult, error) {
	if len(nums) == 0 {
		return nil, nil
	}
	blocks := make([]BlockResult, len(nums))

	workers := len(nums)
	workers = min(workers, a.config.Throttle.Concurrency)

	type job struct {
		num   uint64
		index int
	}

	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				blk, err := a.GetBlock(ctx, j.num)
				blocks[j.index] = BlockResult{Number: j.num, Block: blk}
				if err != nil {
					blocks[j.index].Error = &Error{
						ErrorType: ErrorTypeUnknown,
						Message:   err.Error(),
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i, num := range nums {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{num: num, index: i}:
			}
		}
	}()

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var firstErr error
	for _, b := range blocks {
		if b.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", b.Number, b.Error.Message)
			break
		}
	}
	return blocks, firstErr
}

func (a *AptosIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.GetLatestBlockNumber(ctx)
	return err == nil
}
