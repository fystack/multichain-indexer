package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/status"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const (
	MaxBlockHashSize        = 50
	regularGapRetryAttempts = 2
	regularGapRetryDelay    = time.Second
)

var errRegularRecoveryReorgHandled = errors.New("regular recovery reorg handled")

const blockHashPersistInterval = time.Minute

type RegularWorker struct {
	*BaseWorker
	currentBlock   uint64
	blockHashes    []blockstore.BlockHashEntry
	hashesModified bool
	persistTicker  *time.Ticker
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
	statusRegistry status.StatusRegistry,
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
		statusRegistry,
	)
	rw := &RegularWorker{BaseWorker: worker}
	rw.currentBlock = rw.determineStartingBlock()
	rw.loadBlockHashes()
	return rw
}

func (rw *RegularWorker) Start() {
	rw.logger.Info("Starting regular worker",
		"chain", rw.chain.GetName(),
		"start_block", rw.currentBlock,
	)
	rw.persistTicker = time.NewTicker(blockHashPersistInterval)
	go rw.runBlockHashPersist()
	go rw.run(rw.processRegularBlocks)
}

// Stop stops the worker and cleans up resources
func (rw *RegularWorker) Stop() {
	// Save current block state before stopping
	if rw.currentBlock > 0 {
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetNetworkInternalCode(), rw.currentBlock)
	}
	// Flush block hashes to KV before shutdown
	rw.flushBlockHashes()
	if rw.persistTicker != nil {
		rw.persistTicker.Stop()
	}
	// Call base worker stop to cancel context and clean up
	rw.BaseWorker.Stop()
}

func (rw *RegularWorker) processRegularBlocks() error {
	rw.logger.Info("Starting tick", "currentBlock", rw.currentBlock)

	latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	rw.updateHeadStatus(latest, time.Time{})

	rw.logger.Info("Got latest block", "latest", latest, "current", rw.currentBlock)

	// Lag detection: if we're too far behind, jump to chain head and queue skipped range for catchup
	if rw.skipAheadIfLagging(latest) {
		rw.updateHeadStatus(latest, time.Time{})
		return nil
	}

	if rw.currentBlock > latest {
		rw.logger.Info("Waiting for new blocks...", "current", rw.currentBlock, "latest", latest)
		time.Sleep(rw.config.PollInterval)
		rw.updateHeadStatus(latest, time.Time{})
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
	startTime := time.Now()

	results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end, rw.config.Throttle.Parallel)
	if err != nil {
		return fmt.Errorf("get blocks: %w", err)
	}

	lastSuccess := rw.currentBlock - 1
	var lastSuccessHash string
	var (
		processErr error
		stopTick   bool
	)
	if rw.isReorgCheckRequired() {
		stopTick, processErr = rw.processReorgCheckedBatch(results, end, &lastSuccess, &lastSuccessHash)
	} else {
		for _, res := range results {
			if rw.handleBlockResult(res) {
				lastSuccess = res.Number
				lastSuccessHash = res.Block.Hash
			}
		}
	}

	if stopTick {
		return nil
	}

	indexedAt := time.Time{}
	if lastSuccess >= rw.currentBlock {
		rw.currentBlock = lastSuccess + 1
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetNetworkInternalCode(), lastSuccess)
		indexedAt = time.Now().UTC()

		if lastSuccessHash != "" {
			rw.addBlockHash(lastSuccess, lastSuccessHash)
		}
	}
	rw.updateHeadStatus(latest, indexedAt)

	rw.logger.Info("Processed latest blocks",
		"chain", rw.chain.GetName(),
		"start", originalStart,
		"end", originalEnd,
		"elapsed", time.Since(startTime),
		"last_success", lastSuccess,
		"expected", originalEnd-originalStart+1,
		"got", len(results),
	)
	return processErr
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

		// Batch save all split ranges
		if err := rw.blockStore.SaveCatchupRanges(
			rw.chain.GetNetworkInternalCode(),
			ranges,
		); err != nil {
			rw.logger.Error("Failed to batch save catchup ranges",
				"chain", rw.chain.GetName(),
				"count", len(ranges),
				"error", err,
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

		// Clear all block hashes on reorg
		rw.clearBlockHashes()

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

// addBlockHash adds a block hash to the in-memory array, maintaining max size.
func (rw *RegularWorker) addBlockHash(blockNumber uint64, hash string) {
	rw.blockHashes = append(rw.blockHashes, blockstore.BlockHashEntry{
		BlockNumber: blockNumber,
		Hash:        hash,
	})

	if len(rw.blockHashes) > MaxBlockHashSize {
		rw.blockHashes = rw.blockHashes[len(rw.blockHashes)-MaxBlockHashSize:]
	}

	rw.hashesModified = true
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

// clearBlockHashes clears all block hashes (used on reorg).
func (rw *RegularWorker) clearBlockHashes() {
	rw.blockHashes = rw.blockHashes[:0]
	rw.hashesModified = true
}

// loadBlockHashes loads persisted block hashes from KV store, falling back to an empty slice.
func (rw *RegularWorker) loadBlockHashes() {
	hashes, err := rw.blockStore.GetBlockHashes(rw.chain.GetNetworkInternalCode())
	if err != nil || hashes == nil {
		rw.blockHashes = make([]blockstore.BlockHashEntry, 0, MaxBlockHashSize)
		return
	}
	if len(hashes) > MaxBlockHashSize {
		hashes = hashes[len(hashes)-MaxBlockHashSize:]
	}
	rw.blockHashes = hashes
	rw.logger.Info("Loaded persisted block hashes",
		"chain", rw.chain.GetName(),
		"count", len(hashes),
	)
}

// runBlockHashPersist periodically flushes dirty block hashes to KV store.
func (rw *RegularWorker) runBlockHashPersist() {
	for {
		select {
		case <-rw.ctx.Done():
			return
		case <-rw.persistTicker.C:
			rw.flushBlockHashes()
		}
	}
}

// flushBlockHashes writes block hashes to KV store if they have changed since the last flush.
func (rw *RegularWorker) flushBlockHashes() {
	if !rw.hashesModified {
		return
	}
	if err := rw.blockStore.SaveBlockHashes(rw.chain.GetNetworkInternalCode(), rw.blockHashes); err != nil {
		rw.logger.Error("Failed to persist block hashes",
			"chain", rw.chain.GetName(),
			"error", err,
		)
		return
	}
	rw.hashesModified = false
}

// skipAheadIfLagging checks if the regular worker is too far behind the chain head.
// If so, it queues the skipped range for catchup and jumps currentBlock to chain head.
func (rw *RegularWorker) skipAheadIfLagging(latest uint64) bool {
	maxLag := rw.config.MaxLag
	if maxLag == 0 {
		maxLag = constant.DefaultMaxLag
	}

	if latest <= rw.currentBlock || (latest-rw.currentBlock) <= maxLag {
		return false
	}

	skipStart := rw.currentBlock
	skipEnd := latest - 1

	rw.logger.Warn("Lag threshold exceeded, skipping ahead to chain head",
		"chain", rw.chain.GetName(),
		"current_block", rw.currentBlock,
		"chain_head", latest,
		"lag", latest-rw.currentBlock,
		"max_lag", maxLag,
		"catchup_range", fmt.Sprintf("%d-%d", skipStart, skipEnd),
	)

	ranges := splitCatchupRange(blockstore.CatchupRange{
		Start: skipStart, End: skipEnd, Current: skipStart - 1,
	}, MAX_RANGE_SIZE)

	if err := rw.blockStore.SaveCatchupRanges(
		rw.chain.GetNetworkInternalCode(),
		ranges,
	); err != nil {
		rw.logger.Error("Failed to save skip-ahead catchup ranges",
			"chain", rw.chain.GetName(),
			"count", len(ranges),
			"error", err,
		)
	}

	rw.currentBlock = latest
	_ = rw.blockStore.SaveLatestBlock(rw.chain.GetNetworkInternalCode(), latest-1)
	rw.clearBlockHashes()

	rw.logger.Info("Skip-ahead complete, queued catchup ranges",
		"chain", rw.chain.GetName(),
		"new_current", rw.currentBlock,
		"catchup_ranges", len(ranges),
	)
	return true
}

func (rw *RegularWorker) currentIndexedBlock() uint64 {
	if rw.currentBlock == 0 {
		return 0
	}
	return rw.currentBlock - 1
}

func (rw *RegularWorker) updateHeadStatus(latest uint64, indexedAt time.Time) {
	registry := status.EnsureStatusRegistry(rw.statusRegistry)
	registry.UpdateHead(
		rw.chain.GetName(),
		latest,
		rw.currentIndexedBlock(),
		indexedAt,
	)
}

func (rw *RegularWorker) processReorgCheckedBatch(
	results []indexer.BlockResult,
	end uint64,
	lastSuccess *uint64,
	lastSuccessHash *string,
) (bool, error) {
	expected := rw.currentBlock

	for _, res := range results {
		if expected > end {
			break
		}

		if res.Number != expected {
			rw.logger.Warn("Batch result out of order, switching to single-block recovery",
				"expected", expected,
				"actual", res.Number,
			)
			return rw.recoverRegularGap(expected, end, lastSuccess, lastSuccessHash)
		}

		if res.Error != nil || res.Block == nil {
			rw.logger.Warn("Batch result unresolved, switching to single-block recovery",
				"block", expected,
				"batch_error", blockResultError(res),
			)
			return rw.recoverRegularGap(expected, end, lastSuccess, lastSuccessHash)
		}

		reorg, err := rw.detectAndHandleReorg(&res)
		if err != nil {
			return false, err
		}
		if reorg {
			return true, nil
		}

		if *lastSuccessHash != "" && !matchesParentHash(*lastSuccessHash, res.Block.ParentHash) {
			rw.logger.Warn("Batch continuity broken, switching to single-block recovery",
				"expected", expected,
				"prev_hash", *lastSuccessHash,
				"curr_parent", res.Block.ParentHash,
			)
			return rw.recoverRegularGap(expected, end, lastSuccess, lastSuccessHash)
		}

		if rw.handleBlockResult(res) {
			*lastSuccess = res.Number
			*lastSuccessHash = res.Block.Hash
			expected = res.Number + 1
		}
	}

	if expected <= end {
		rw.logger.Warn("Batch returned incomplete results, switching to single-block recovery",
			"expected", expected,
			"end", end,
			"received", len(results),
		)
		return rw.recoverRegularGap(expected, end, lastSuccess, lastSuccessHash)
	}

	return false, nil
}

func (rw *RegularWorker) recoverRegularGap(
	start, end uint64,
	lastSuccess *uint64,
	lastSuccessHash *string,
) (bool, error) {
	rw.logger.Warn("Recovering regular gap with failover-aware single-block fetch",
		"start", start,
		"end", end,
		"attempts", regularGapRetryAttempts,
	)

	for blockNumber := start; blockNumber <= end; blockNumber++ {
		if err := rw.recoverRegularBlock(blockNumber, lastSuccess, lastSuccessHash); err != nil {
			if errors.Is(err, errRegularRecoveryReorgHandled) {
				return true, nil
			}

			rw.handleBlockResult(indexer.BlockResult{
				Number: blockNumber,
				Error: &indexer.Error{
					ErrorType: indexer.ErrorTypeUnknown,
					Message:   err.Error(),
				},
			})
			return false, fmt.Errorf("recover block %d: %w", blockNumber, err)
		}
	}

	return false, nil
}

func (rw *RegularWorker) recoverRegularBlock(
	blockNumber uint64,
	lastSuccess *uint64,
	lastSuccessHash *string,
) error {
	var lastErr error

	for attempt := 1; attempt <= regularGapRetryAttempts; attempt++ {
		res, err := rw.fetchRegularBlock(blockNumber)
		if err == nil {
			reorg, reorgErr := rw.detectAndHandleReorg(&res)
			if reorgErr != nil {
				return reorgErr
			}
			if reorg {
				return errRegularRecoveryReorgHandled
			}

			if *lastSuccessHash != "" && !matchesParentHash(*lastSuccessHash, res.Block.ParentHash) {
				err = fmt.Errorf(
					"continuity mismatch for block %d: expected parent %s, got %s",
					blockNumber,
					*lastSuccessHash,
					res.Block.ParentHash,
				)
			} else if rw.handleBlockResult(res) {
				*lastSuccess = res.Number
				*lastSuccessHash = res.Block.Hash
				return nil
			} else {
				err = fmt.Errorf("failed to process recovered block %d", blockNumber)
			}
		}

		lastErr = err
		rw.logger.Warn("Single-block recovery failed",
			"block", blockNumber,
			"attempt", attempt,
			"max_attempts", regularGapRetryAttempts,
			"error", err,
		)

		if attempt < regularGapRetryAttempts {
			select {
			case <-rw.ctx.Done():
				return rw.ctx.Err()
			case <-time.After(regularGapRetryDelay):
			}
		}
	}

	return lastErr
}

func (rw *RegularWorker) fetchRegularBlock(blockNumber uint64) (indexer.BlockResult, error) {
	block, err := rw.chain.GetBlock(rw.ctx, blockNumber)
	if err != nil {
		return indexer.BlockResult{Number: blockNumber}, err
	}
	if block == nil {
		return indexer.BlockResult{Number: blockNumber}, fmt.Errorf("nil block result for %d", blockNumber)
	}
	return indexer.BlockResult{
		Number: blockNumber,
		Block:  block,
	}, nil
}

func checkContinuity(prev, curr indexer.BlockResult) bool {
	if prev.Error != nil || curr.Error != nil || prev.Block == nil || curr.Block == nil {
		return false
	}
	return prev.Block.Hash == curr.Block.ParentHash
}

func blockResultNumber(res indexer.BlockResult) uint64 {
	if res.Block != nil {
		return res.Block.Number
	}
	return res.Number
}

func blockResultHash(res indexer.BlockResult) string {
	if res.Block != nil {
		return res.Block.Hash
	}
	return ""
}

func blockResultError(res indexer.BlockResult) string {
	if res.Error != nil {
		return res.Error.Message
	}
	if res.Block == nil {
		return "nil block"
	}
	return ""
}

func matchesParentHash(prevHash, parentHash string) bool {
	return prevHash == parentHash
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
