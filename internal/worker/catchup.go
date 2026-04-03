package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/status"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/catchupstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const (
	MAX_RANGE_SIZE         = 20
	CATCHUP_WORKERS        = 3 // Number of parallel workers
	PROGRESS_SAVE_INTERVAL = 1 // Save progress every N batches
	catchupPanicRetryDelay = time.Second
)

type CatchupWorker struct {
	*BaseWorker
	blockRanges []blockstore.CatchupRange
	workerPool  chan struct{}
	progressMu  sync.Mutex
}

func NewCatchupWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore blockstore.Store,
	catchupStore catchupstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
	statusRegistry status.StatusRegistry,
) *CatchupWorker {
	worker := newWorkerWithMode(
		ctx,
		chain,
		cfg,
		kv,
		blockStore,
		catchupStore,
		emitter,
		pubkeyStore,
		ModeCatchup,
		failedChan,
		statusRegistry,
	)
	cw := &CatchupWorker{
		BaseWorker: worker,
		workerPool: make(chan struct{}, CATCHUP_WORKERS),
	}
	cw.blockRanges = cw.loadCatchupProgress()
	return cw
}

func (cw *CatchupWorker) Start() {
	totalBlocks := uint64(0)
	for _, r := range cw.blockRanges {
		totalBlocks += r.End - r.Start + 1
	}

	cw.logger.Info("Starting optimized catchup worker",
		"chain", cw.chain.GetName(),
		"ranges", len(cw.blockRanges),
		"total_blocks", totalBlocks,
		"parallel_workers", CATCHUP_WORKERS,
	)
	cw.executeWithRecovery("catchup loop", cw.runCatchup)
}

// runCatchup is a tight loop that processes catchup ranges without PollInterval delays.
// Unlike the base run() method, it exits once all ranges are processed.
func (cw *CatchupWorker) runCatchup() {
	for {
		select {
		case <-cw.ctx.Done():
			cw.logger.Info("Context done, stopping catchup worker loop")
			return
		default:
		}

		err := cw.executeRecoverable("catchup pass", cw.processCatchupBlocksParallel)
		if err != nil {
			cw.logger.Error("Catchup job error", "err", err)
			_ = cw.emitter.EmitError(cw.chain.GetName(), err)
			if _, ok := err.(*recoveredPanicError); ok {
				select {
				case <-cw.ctx.Done():
					return
				case <-time.After(catchupPanicRetryDelay):
				}
			}
			continue
		}

		// If no ranges remain, catchup is done
		if len(cw.blockRanges) == 0 {
			cw.logger.Info("Catchup completed, no more ranges to process",
				"chain", cw.chain.GetName(),
			)
			return
		}
	}
}

func (cw *CatchupWorker) loadCatchupProgress() []blockstore.CatchupRange {
	// Load existing catchup ranges from the store. The catchup worker only loads
	// ranges; creating new ranges is the responsibility of the regular worker
	// (via determineStartingBlock or skipAheadIfLagging).
	progress, err := cw.catchupStore.GetProgress(cw.ctx, cw.chain.GetNetworkInternalCode())
	if err != nil {
		cw.logger.Warn("Failed to load catchup progress",
			"chain", cw.chain.GetName(),
			"error", err,
		)
		return nil
	}

	cw.logger.Info("Loaded catchup progress",
		"chain", cw.chain.GetName(),
		"progress_ranges", len(progress),
	)
	return progress
}

// Split large ranges into smaller, more manageable chunks
func (cw *CatchupWorker) splitLargeRange(r blockstore.CatchupRange) []blockstore.CatchupRange {
	subRanges := splitCatchupRange(r, MAX_RANGE_SIZE)

	if len(subRanges) > 1 {
		cw.logger.Info("Split large catchup range",
			"chain", cw.chain.GetName(),
			"original_range", fmt.Sprintf("%d-%d", r.Start, r.End),
			"original_size", r.End-r.Start+1,
			"sub_ranges", len(subRanges),
		)
	}

	return subRanges
}

func (cw *CatchupWorker) processCatchupBlocksParallel() error {
	if len(cw.blockRanges) == 0 {
		cw.logger.Info("No catchup ranges to process")
		return nil
	}

	// Claim multiple ranges in parallel so multiple instances can split work.
	var wg sync.WaitGroup

	// Start parallel workers
	for i := 0; i < CATCHUP_WORKERS; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer cw.recoverPanic(fmt.Sprintf("catchup range worker %d", workerID))
			cw.logger.Debug("Starting catchup worker", "worker_id", workerID)

			for {
				r, ok := cw.claimNextRange()
				if !ok {
					return
				}
				if err := cw.processRange(r, workerID); err != nil {
					cw.logger.Error("Failed to process range",
						"worker_id", workerID,
						"range", fmt.Sprintf("%d-%d", r.Start, r.End),
						"error", err,
					)
				}
			}
		}(i)
	}

	wg.Wait()
	cw.logger.Info("Catchup processing completed")

	// Reload ranges to check for any remaining work
	cw.blockRanges = cw.loadCatchupProgress()
	return nil
}

func (cw *CatchupWorker) claimNextRange() (blockstore.CatchupRange, bool) {
	claimed, err := cw.catchupStore.GetNextRange(cw.ctx, cw.chain.GetNetworkInternalCode())
	if err != nil {
		cw.logger.Warn("Failed to claim catchup range",
			"chain", cw.chain.GetName(),
			"error", err,
		)
		return blockstore.CatchupRange{}, false
	}
	if claimed == nil {
		return blockstore.CatchupRange{}, false
	}

	cw.logger.Info("Claimed catchup range",
		"chain", cw.chain.GetName(),
		"range", fmt.Sprintf("%d-%d", claimed.Start, claimed.End),
		"current", claimed.Current,
	)
	return *claimed, true
}

func (cw *CatchupWorker) processRange(r blockstore.CatchupRange, workerID int) error {
	batchCount := 0
	startTime := time.Now()

	cw.logger.Info("Processing catchup range",
		"worker_id", workerID,
		"range", fmt.Sprintf("%d-%d", r.Start, r.End),
		"size", r.End-r.Start+1,
	)

	// Get current progress
	current := r.Current + 1
	if current > r.End {
		cw.logger.Debug("Range already completed",
			"worker_id", workerID,
			"range", fmt.Sprintf("%d-%d", r.Start, r.End),
		)
		return cw.completeRange(r)
	}

	batchSize := uint64(cw.config.Throttle.BatchSize)
	lastSuccess := current - 1

	for current <= r.End {
		// Check context cancellation
		select {
		case <-cw.ctx.Done():
			cw.logger.Info("Context cancelled, saving progress before stopping",
				"worker_id", workerID,
				"range", fmt.Sprintf("%d-%d", r.Start, r.End),
				"current_position", lastSuccess,
			)
			cw.saveProgress(r, lastSuccess)
			return cw.ctx.Err()
		default:
		}

		end := min(current+batchSize-1, r.End)

		// Process batch
		results, err := cw.chain.GetBlocks(cw.ctx, current, end, true)
		if err != nil {
			cw.logger.Warn("Failed to get blocks, retrying",
				"worker_id", workerID,
				"range", fmt.Sprintf("%d-%d", current, end),
				"error", err,
			)
			time.Sleep(time.Second) // Brief pause before retry
			continue
		}

		// Process results
		batchSuccess := current - 1
		for _, res := range results {
			if res.Error != nil && res.Error.ErrorType == indexer.ErrorTypeBlockNotFound && cw.chain.GetNetworkType() == enum.NetworkTypeSol {
				// Solana skipped slots are normal — the validator didn't produce a block
				// for this slot. Skip without retry.
				cw.logger.Debug("Solana skipped slot, no retry needed", "slot", res.Number)
				cw.notifyObserver(res.Number, BlockStatusNotFound)
				if res.Number > batchSuccess {
					batchSuccess = res.Number
				}
				continue
			}
			if cw.handleBlockResult(res) && res.Number > batchSuccess {
				batchSuccess = res.Number
			}
		}

		if batchSuccess >= current {
			lastSuccess = batchSuccess
			current = batchSuccess + 1
		} else {
			// If no progress made, move to next batch to avoid infinite loop
			current = end + 1
		}

		batchCount++

		// Save progress periodically
		if batchCount%PROGRESS_SAVE_INTERVAL == 0 {
			cw.saveProgress(r, min(lastSuccess, r.End))

			// Log progress
			processed := lastSuccess - r.Start + 1
			total := r.End - r.Start + 1
			progress := float64(processed) / float64(total) * 100
			elapsed := time.Since(startTime)

			cw.logger.Info("Catchup progress",
				"worker_id", workerID,
				"range", fmt.Sprintf("%d-%d", r.Start, r.End),
				"current", lastSuccess,
				"progress", fmt.Sprintf("%.1f%%", progress),
				"processed", processed,
				"total", total,
				"elapsed", elapsed.Truncate(time.Second),
				"batches", batchCount,
			)
		}
	}

	// Final progress save
	cw.saveProgress(r, min(lastSuccess, r.End))

	elapsed := time.Since(startTime)
	cw.logger.Info("Catchup range completed",
		"worker_id", workerID,
		"range", fmt.Sprintf("%d-%d", r.Start, r.End),
		"blocks_processed", r.End-r.Start+1,
		"elapsed", elapsed.Truncate(time.Second),
		"batches", batchCount,
	)

	return cw.completeRange(r)
}

func (cw *CatchupWorker) saveProgress(r blockstore.CatchupRange, current uint64) {
	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()
	cw.logger.Debug("Saving catchup progress",
		"chain", cw.chain.GetName(),
		"range", fmt.Sprintf("%d-%d", r.Start, r.End),
		"current", current,
	)
	current = min(current, r.End)
	if err := cw.catchupStore.SaveProgress(cw.ctx, cw.chain.GetNetworkInternalCode(), r.Start, r.End, current); err != nil {
		cw.logger.Warn("Failed to save catchup progress",
			"chain", cw.chain.GetName(),
			"range", fmt.Sprintf("%d-%d", r.Start, r.End),
			"current", current,
			"error", err,
		)
		return
	}
	for i := range cw.blockRanges {
		if cw.blockRanges[i].Start == r.Start && cw.blockRanges[i].End == r.End {
			cw.blockRanges[i].Current = current
			break
		}
	}
}

func (cw *CatchupWorker) completeRange(r blockstore.CatchupRange) error {
	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()

	cw.logger.Info("Completing catchup range",
		"chain", cw.chain.GetName(),
		"range", fmt.Sprintf("%d-%d", r.Start, r.End),
	)

	if err := cw.catchupStore.DeleteRange(cw.ctx, cw.chain.GetNetworkInternalCode(), r.Start, r.End); err != nil {
		cw.logger.Warn("Failed to delete catchup range",
			"chain", cw.chain.GetName(),
			"range", fmt.Sprintf("%d-%d", r.Start, r.End),
			"error", err,
		)
		return err
	}
	// Remove from local ranges
	for i, existing := range cw.blockRanges {
		if existing.Start == r.Start && existing.End == r.End {
			cw.blockRanges = append(cw.blockRanges[:i], cw.blockRanges[i+1:]...)
			break
		}
	}

	return nil
}

func (cw *CatchupWorker) Close() error {
	cw.logger.Info("Closing catchup worker, saving progress...",
		"chain", cw.chain.GetName(),
		"ranges", len(cw.blockRanges),
	)

	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()

	if len(cw.blockRanges) == 0 {
		return nil
	}

	// Cap Current at End for each range before batch saving
	rangesToSave := make([]blockstore.CatchupRange, len(cw.blockRanges))
	for i, r := range cw.blockRanges {
		rangesToSave[i] = blockstore.CatchupRange{
			Start:   r.Start,
			End:     r.End,
			Current: min(r.Current, r.End),
		}
		cw.logger.Debug("Saving final catchup progress on close",
			"range", fmt.Sprintf("%d-%d", r.Start, r.End),
			"current", rangesToSave[i].Current,
		)
	}

	if err := cw.catchupStore.SaveRanges(cw.ctx, cw.chain.GetNetworkInternalCode(), rangesToSave); err != nil {
		cw.logger.Error("Failed to batch save progress on close",
			"chain", cw.chain.GetName(),
			"ranges", len(rangesToSave),
			"error", err,
		)
	}

	return nil
}
