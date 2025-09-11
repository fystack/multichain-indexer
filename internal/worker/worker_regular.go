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

type RegularWorker struct {
	*BaseWorker
}

func NewRegularWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore *blockstore.Store,
	emitter *events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *RegularWorker {
	worker := newWorkerWithMode(ctx, chain, cfg, kv, blockStore, emitter, pubkeyStore, ModeRegular, failedChan)
	rw := &RegularWorker{BaseWorker: worker}
	rw.currentBlock = rw.determineStartingBlock()
	return rw
}

func (rw *RegularWorker) Start() {
	rw.logger.Info("Starting regular worker",
		"chain", rw.chain.GetName(),
		"start_block", rw.currentBlock,
	)
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

	end := min(rw.currentBlock+uint64(rw.config.BatchSize)-1, latest)
	results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get blocks: %w", err)
	}

	lastSuccess := rw.currentBlock - 1
	startTime := time.Now()

	for i, res := range results {
		if res.Block == nil || res.Error != nil {
			continue
		}
		// cross-batch reorg detection
		if rw.isReorgCheckRequired() {
			if reorg, err := rw.detectAndHandleReorg(&res); err != nil {
				return err
			} else if reorg {
				return nil // rollback done, stop this tick
			}
		}
		// intra-batch continuity
		if i > 0 && rw.isReorgCheckRequired() {
			if !checkContinuity(results[i-1], res) {
				rw.logger.Warn("Batch continuity broken, will retry next tick",
					"prev", results[i-1].Block.Number,
					"prev_hash", results[i-1].Block.Hash,
					"curr", res.Block.Number,
					"curr_parent", res.Block.ParentHash,
				)
				break
			}
		}
		if rw.handleBlockResult(res) {
			lastSuccess = res.Number
		}
	}

	if lastSuccess >= rw.currentBlock {
		rw.currentBlock = lastSuccess + 1
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetName(), lastSuccess)
	}

	rw.logger.Info("Processed latest blocks",
		"chain", rw.chain.GetName(),
		"start", rw.currentBlock,
		"end", end,
		"elapsed", time.Since(startTime),
		"last_success", lastSuccess,
		"expected", end-rw.currentBlock+1,
		"got", len(results),
	)
	return nil
}

func (rw *RegularWorker) determineStartingBlock() uint64 {
	if latest, err := rw.blockStore.GetLatestBlock(rw.chain.GetName()); err == nil && latest > 0 {
		return latest
	}
	if rw.config.FromLatest {
		if chainLatest, err := rw.chain.GetLatestBlockNumber(rw.ctx); err == nil {
			return chainLatest
		}
	}
	return uint64(rw.config.StartBlock)
}

func (rw *RegularWorker) detectAndHandleReorg(res *indexer.BlockResult) (bool, error) {
	prevNum := res.Block.Number - 1
	storedHash, _ := rw.blockStore.GetBlockHash(rw.chain.GetName(), prevNum)
	if storedHash != "" && storedHash != res.Block.ParentHash {
		rollbackWindow := uint64(rw.config.ReorgRollbackWindow)
		if rollbackWindow == 0 {
			rollbackWindow = constant.DefaultReorgRollbackWindow
		}
		reorgStart := uint64(1)
		if prevNum > rollbackWindow {
			reorgStart = prevNum - rollbackWindow
		}
		rw.logger.Warn("Reorg detected; rolling back",
			"chain", rw.chain.GetName(),
			"at_block", prevNum,
			"expected_parent", storedHash,
			"actual_parent", res.Block.ParentHash,
			"rollback_start", reorgStart,
			"rollback_end", prevNum,
		)
		if err := rw.blockStore.DeleteBlockHashesInRange(rw.chain.GetName(), reorgStart, prevNum); err != nil {
			return true, fmt.Errorf("delete block hashes: %w", err)
		}
		if err := rw.blockStore.SaveLatestBlock(rw.chain.GetName(), reorgStart-1); err != nil {
			return true, fmt.Errorf("save latest block: %w", err)
		}
		rw.currentBlock = reorgStart
		return true, nil
	}
	return false, nil
}

func checkContinuity(prev, curr indexer.BlockResult) bool {
	return prev.Block.Hash == curr.Block.ParentHash
}

func (rw *RegularWorker) isReorgCheckRequired() bool {
	return rw.chain.GetChainType() == enum.ChainTypeEVM
}
