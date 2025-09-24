package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const (
	MaxBlockHashSize = 20
)

type RegularWorker struct {
	*BaseWorker
	currentBlock uint64
	blockHashes  []BlockHashEntry
}

type BlockHashEntry struct {
	BlockNumber uint64
	Hash        string
}

func NewRegularWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *RegularWorker {
	worker := newWorkerWithMode(
		ctx,
		chain,
		cfg,
		kv,
		blockStore,
		emitter,
		pubkeyStore,
		ModeRegular,
		failedChan,
	)
	rw := &RegularWorker{BaseWorker: worker}
	rw.currentBlock = rw.determineStartingBlock()
	rw.blockHashes = make([]BlockHashEntry, 0, MaxBlockHashSize)
	return rw
}

func (rw *RegularWorker) Start() {
	rw.logger.Info("Starting regular worker",
		"chain", rw.chain.GetName(),
		"start_block", rw.currentBlock,
	)
	go rw.run(rw.processRegularBlocks)
}

// Stop stops the worker and cleans up resources
func (rw *RegularWorker) Stop() {
	// Save current block state before stopping
	if rw.currentBlock > 0 {
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetName(), rw.currentBlock)
	}
	rw.clearBlockHashes()
	// Call base worker stop to cancel context and clean up
	rw.BaseWorker.Stop()
}

func (rw *RegularWorker) processRegularBlocks() error {
	latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}

	if rw.currentBlock > latest {
		rw.logger.Debug("Waiting for new blocks...")
		time.Sleep(rw.config.PollInterval)
		return nil
	}

	end := min(rw.currentBlock+uint64(rw.config.Throttle.BatchSize)-1, latest)
	rw.logger.Info(
		"Processing range",
		"chain",
		rw.chain.GetName(),
		"start",
		rw.currentBlock,
		"end",
		end,
		"size",
		end-rw.currentBlock+1,
	)

	// Store original range for logging
	originalStart := rw.currentBlock
	originalEnd := end

	results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end, false)
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

		// Add block hash to array
		if len(results) > 0 && results[len(results)-1].Block != nil {
			rw.addBlockHash(lastSuccess, results[len(results)-1].Block.Hash)
		}
	}

	rw.logger.Info("Processed latest blocks",
		"chain", rw.chain.GetName(),
		"start", originalStart,
		"end", originalEnd,
		"elapsed", time.Since(startTime),
		"last_success", lastSuccess,
		"expected", originalEnd-originalStart+1,
		"got", len(results),
	)
	return nil
}

func (rw *RegularWorker) determineStartingBlock() uint64 {
	chainLatest, err1 := rw.chain.GetLatestBlockNumber(rw.ctx)
	kvLatest, err2 := rw.blockStore.GetLatestBlock(rw.chain.GetName())

	if err1 != nil && err2 != nil {
		rw.logger.Warn("Cannot get latest block from chain or KV, using config.StartBlock",
			"chain", rw.chain.GetName(),
			"startBlock", rw.config.StartBlock,
		)
		return uint64(rw.config.StartBlock)
	}

	if err1 != nil && kvLatest > 0 {
		rw.logger.Warn("Chain RPC failed, resuming from KV latest",
			"chain", rw.chain.GetName(),
			"kvLatest", kvLatest,
		)
		return kvLatest
	}

	if err2 != nil || kvLatest == 0 {
		return chainLatest
	}

	if chainLatest > kvLatest {
		start := kvLatest + 1
		end := chainLatest
		_ = rw.blockStore.SaveCatchupProgress(rw.chain.GetName(), start, end, start-1)

		rw.logger.Info("Queued catchup range",
			"chain", rw.chain.GetName(),
			"start", start, "end", end,
		)
	}

	return chainLatest
}

func (rw *RegularWorker) detectAndHandleReorg(res *indexer.BlockResult) (bool, error) {
	prevNum := res.Block.Number - 1
	storedHash := rw.getBlockHash(prevNum)
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

		// Clear all block hashes on reorg
		rw.clearBlockHashes()

		if err := rw.blockStore.SaveLatestBlock(rw.chain.GetName(), reorgStart-1); err != nil {
			return true, fmt.Errorf("save latest block: %w", err)
		}
		rw.currentBlock = reorgStart
		return true, nil
	}
	return false, nil
}

func (rw *RegularWorker) isReorgCheckRequired() bool {
	return rw.chain.GetNetworkType() == enum.NetworkTypeEVM
}

// addBlockHash adds a block hash to the array, maintaining max size
func (rw *RegularWorker) addBlockHash(blockNumber uint64, hash string) {
	entry := BlockHashEntry{
		BlockNumber: blockNumber,
		Hash:        hash,
	}

	// Add to the end
	rw.blockHashes = append(rw.blockHashes, entry)

	// Remove oldest entries if we exceed max size
	if len(rw.blockHashes) > MaxBlockHashSize {
		rw.blockHashes = rw.blockHashes[len(rw.blockHashes)-MaxBlockHashSize:]
	}
}

// getBlockHash retrieves a block hash by block number
func (rw *RegularWorker) getBlockHash(blockNumber uint64) string {
	for _, entry := range rw.blockHashes {
		if entry.BlockNumber == blockNumber {
			return entry.Hash
		}
	}
	return ""
}

// clearBlockHashes clears all block hashes (used on reorg)
func (rw *RegularWorker) clearBlockHashes() {
	rw.blockHashes = rw.blockHashes[:0] // Clear slice but keep capacity
}

func checkContinuity(prev, curr indexer.BlockResult) bool {
	return prev.Block.Hash == curr.Block.ParentHash
}
