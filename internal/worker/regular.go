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
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetNetworkInternalCode(), rw.currentBlock)
	}
	rw.clearBlockHashes()
	rw.ClearBlockCache()
	// Call base worker stop to cancel context and clean up
	rw.BaseWorker.Stop()
}

func (rw *RegularWorker) processRegularBlocks() error {
	rw.logger.Info("Starting tick", "currentBlock", rw.currentBlock)

	latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}

	// Bitcoin-specific: Only index confirmed blocks
	if rw.chain.GetNetworkType() == enum.NetworkTypeBtc {
		btcIndexer, ok := rw.chain.(*indexer.BitcoinIndexer)
		if ok {
			confirmedHeight, err := btcIndexer.GetConfirmedHeight(rw.ctx)
			if err != nil {
				return fmt.Errorf("get confirmed height: %w", err)
			}
			rw.logger.Info("Got confirmed height for Bitcoin",
				"latest", latest,
				"confirmed", confirmedHeight,
				"current", rw.currentBlock)
			latest = confirmedHeight

			if rw.currentBlock > latest {
				rw.logger.Info("Waiting for confirmations",
					"current", rw.currentBlock,
					"confirmed_height", latest)
				time.Sleep(rw.config.PollInterval)
				return nil
			}
		}
	} else {
		rw.logger.Info("Got latest block", "latest", latest, "current", rw.currentBlock)
	}

	if rw.currentBlock > latest {
		rw.logger.Info("Waiting for new blocks...", "current", rw.currentBlock, "latest", latest)
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

	results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end, rw.config.Throttle.Parallel)
	if err != nil {
		return fmt.Errorf("get blocks: %w", err)
	}

	lastSuccess := rw.currentBlock - 1
	var lastSuccessHash string
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
			lastSuccessHash = res.Block.Hash
			rw.CacheBlock(res.Block)
		}
	}

	if lastSuccess >= rw.currentBlock {
		rw.currentBlock = lastSuccess + 1
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetNetworkInternalCode(), lastSuccess)

		if lastSuccessHash != "" {
			rw.addBlockHash(lastSuccess, lastSuccessHash)
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
	kvLatest, err2 := rw.blockStore.GetLatestBlock(rw.chain.GetNetworkInternalCode())

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

		// Split the range into manageable chunks
		ranges := splitCatchupRange(blockstore.CatchupRange{
			Start: start, End: end, Current: start - 1,
		}, MAX_RANGE_SIZE)

		// Save each split range
		for _, r := range ranges {
			_ = rw.blockStore.SaveCatchupProgress(
				rw.chain.GetNetworkInternalCode(),
				r.Start,
				r.End,
				r.Current,
			)
		}

		rw.logger.Info("Queued catchup ranges",
			"chain", rw.chain.GetName(),
			"gap", fmt.Sprintf("%d-%d", start, end),
			"ranges_created", len(ranges),
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

		// Emit orphan UTXO events for blocks being rolled back
		rw.EmitOrphanUTXOs(reorgStart, prevNum)

		// Clear all block hashes and cache on reorg
		rw.clearBlockHashes()
		rw.ClearBlockCache()

		if err := rw.blockStore.SaveLatestBlock(rw.chain.GetNetworkInternalCode(), reorgStart-1); err != nil {
			return true, fmt.Errorf("save latest block: %w", err)
		}
		rw.currentBlock = reorgStart
		return true, nil
	}
	return false, nil
}

func (rw *RegularWorker) isReorgCheckRequired() bool {
	networkType := rw.chain.GetNetworkType()
	return networkType == enum.NetworkTypeEVM || networkType == enum.NetworkTypeBtc
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

// splitCatchupRange splits a large catchup range into smaller, manageable chunks
func splitCatchupRange(r blockstore.CatchupRange, maxSize uint64) []blockstore.CatchupRange {
	if r.End <= r.Start {
		return []blockstore.CatchupRange{r}
	}

	totalBlocks := r.End - r.Start + 1
	if totalBlocks <= maxSize {
		return []blockstore.CatchupRange{r}
	}

	var ranges []blockstore.CatchupRange
	current := r.Start

	for current <= r.End {
		end := min(current+maxSize-1, r.End)

		// Create a new range with the same Current position as the original
		// but adjusted to be within the new range bounds
		newCurrent := r.Current
		if newCurrent < current {
			newCurrent = current - 1
		} else if newCurrent > end {
			newCurrent = end
		}

		ranges = append(ranges, blockstore.CatchupRange{
			Start:   current,
			End:     end,
			Current: newCurrent,
		})

		current = end + 1
	}

	return ranges
}

