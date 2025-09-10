package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
)

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

type RegularWorker struct {
	*BaseWorker
}

// NewWorker creates a worker for regular indexing
func NewRegularWorker(ctx context.Context, chain indexer.Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *blockstore.Store, emitter *events.Emitter, pubkeyStore pubkeystore.Store, failedChan chan FailedBlockEvent) *RegularWorker {
	worker := newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, pubkeyStore, ModeRegular, failedChan)
	regular := &RegularWorker{
		BaseWorker: worker,
	}
	regular.currentBlock = regular.determineStartingBlock()
	return regular
}

// Start the worker
func (rw *RegularWorker) Start() {
	rw.logger.Info("Starting regular worker", "chain", rw.chain.GetName(), "start_block", rw.currentBlock)
	go rw.run(rw.processRegularBlocks)
}

func (rw *RegularWorker) processRegularBlocks() error {
	latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	if rw.currentBlock > latest {
		time.Sleep(3 * time.Second)
		return nil
	}

	end := minUint64(rw.currentBlock+uint64(rw.config.BatchSize)-1, latest)
	lastSuccess := rw.currentBlock - 1

	start := time.Now()
	results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get batch blocks: %w", err)
	}
	elapsed := time.Since(start)
	rw.logger.Info(
		"Processing latest blocks",
		"chain",
		rw.chain.GetName(),
		"start",
		rw.currentBlock,
		"end",
		end,
		"elapsed",
		elapsed,
		"last_success",
		lastSuccess,
		"expected number of blocks",
		end-rw.currentBlock+1,
		"actual blocks",
		len(results),
	)

	// Reorg detection: ensure parent hash continuity
	for i, result := range results {
		if result.Block == nil || result.Error != nil {
			continue
		}

		if rw.isReorgCheckRequired() {
			if reorg, err := rw.detectAndHandleReorg(&result); err != nil {
				return err
			} else if reorg {
				return nil
			}
		}

		if rw.handleBlockResult(result) {
			lastSuccess = result.Number
		}

		// intra-batch continuity check
		if rw.isReorgCheckRequired() && i > 0 && results[i-1].Block != nil && result.Block != nil {
			if results[i-1].Block.Hash != result.Block.ParentHash {
				rw.logger.Warn("Batch continuity broken, will retry next tick",
					"prev", results[i-1].Block.Number,
					"prev_hash", results[i-1].Block.Hash,
					"curr", result.Block.Number,
					"curr_parent", result.Block.ParentHash,
				)
				break
			}
		}
	}

	if lastSuccess >= rw.currentBlock {
		rw.currentBlock = lastSuccess + 1
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetName(), lastSuccess)
	}
	return nil
}

// determineStartingBlock determines the starting block for the regular worker
// it will use the latest block from the block store if it exists, otherwise it will use the start block from the config
// if from latest is true, it will use the latest block from the chain
func (rw *RegularWorker) determineStartingBlock() uint64 {
	latestBlock, err := rw.blockStore.GetLatestBlock(rw.chain.GetName())
	if err != nil || latestBlock == 0 {
		latestBlock = uint64(rw.config.StartBlock)
	}
	if rw.config.FromLatest {
		chainLatest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
		if err == nil {
			latestBlock = chainLatest
		}
	}
	return latestBlock
}

func (rw *RegularWorker) detectAndHandleReorg(result *indexer.BlockResult) (bool, error) {
	if result.Block == nil || result.Error != nil {
		return false, nil
	}

	if result.Block.Number > rw.currentBlock {
		prevNum := result.Block.Number - 1
		storedHash, _ := rw.blockStore.GetBlockHash(rw.chain.GetName(), prevNum)

		if storedHash != "" && storedHash != result.Block.ParentHash {
			// Reorg detected
			rollbackWindow := uint64(rw.config.ReorgRollbackWindow)
			if rollbackWindow == 0 {
				rollbackWindow = constant.DefaultReorgRollbackWindow
			}
			var reorgStart uint64
			if prevNum > rollbackWindow {
				reorgStart = prevNum - rollbackWindow
			} else {
				reorgStart = 1
			}

			rw.logger.Warn("Reorg detected; rolling back",
				"chain", rw.chain.GetName(),
				"at_block", prevNum,
				"expected_parent", storedHash,
				"actual_parent", result.Block.ParentHash,
				"rollback_start", reorgStart,
				"rollback_end", prevNum,
			)

			// Delete hashes and adjust latest
			if err := rw.blockStore.DeleteBlockHashesInRange(rw.chain.GetName(), reorgStart, prevNum); err != nil {
				return true, fmt.Errorf("delete block hashes: %w", err)
			}
			if err := rw.blockStore.SaveLatestBlock(rw.chain.GetName(), reorgStart-1); err != nil {
				return true, fmt.Errorf("save latest block: %w", err)
			}

			rw.currentBlock = reorgStart
			return true, nil
		}
	}

	return false, nil
}

func (rw *RegularWorker) isReorgCheckRequired() bool {
	name := rw.chain.GetChainType()
	switch name {
	case enum.ChainTypeEVM:
		return true
	default:
		return false
	}
}
