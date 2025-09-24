package worker

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const (
	MAX_RANGE_SIZE         = 20
	CATCHUP_WORKERS        = 3 // Number of parallel workers
	PROGRESS_SAVE_INTERVAL = 1 // Save progress every N batches
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
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *CatchupWorker {
	worker := newWorkerWithMode(
		ctx,
		chain,
		cfg,
		kv,
		blockStore,
		emitter,
		pubkeyStore,
		ModeCatchup,
		failedChan,
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
	go cw.run(cw.processCatchupBlocksParallel)
}

func (cw *CatchupWorker) loadCatchupProgress() []blockstore.CatchupRange {
	var ranges []blockstore.CatchupRange

	if progress, err := cw.blockStore.GetCatchupProgress(cw.chain.GetName()); err == nil {
		cw.logger.Info("Loading existing catchup progress",
			"chain", cw.chain.GetName(),
			"progress_ranges", len(progress),
		)
		for _, p := range progress {
			// Split large ranges into smaller chunks
			subRanges := cw.splitLargeRange(p)
			ranges = append(ranges, subRanges...)
		}
	} else {
		cw.logger.Warn("Failed to load catchup progress, will create new range",
			"chain", cw.chain.GetName(),
			"error", err,
		)
	}

	if len(ranges) == 0 {
		if latest, err1 := cw.blockStore.GetLatestBlock(cw.chain.GetName()); err1 == nil {
			if head, err2 := cw.chain.GetLatestBlockNumber(cw.ctx); err2 == nil && head > latest {
				start, end := latest+1, head
				cw.logger.Info("Creating new catchup range",
					"chain", cw.chain.GetName(),
					"latest_block", latest,
					"head_block", head,
					"catchup_start", start, "catchup_end", end,
					"blocks_to_catchup", end-latest,
				)

				// Split new range if it's too large
				newRanges := cw.splitLargeRange(blockstore.CatchupRange{
					Start: start, End: end, Current: start - 1,
				})

				for _, nr := range newRanges {
					_ = cw.blockStore.SaveCatchupProgress(
						cw.chain.GetName(),
						nr.Start,
						nr.End,
						nr.Current,
					)
				}
				ranges = append(ranges, newRanges...)
			}
		}
	}

	return mergeRanges(ranges)
}

// Split large ranges into smaller, more manageable chunks
func (cw *CatchupWorker) splitLargeRange(r blockstore.CatchupRange) []blockstore.CatchupRange {
	rangeSize := r.End - r.Start + 1
	if rangeSize <= MAX_RANGE_SIZE {
		return []blockstore.CatchupRange{r}
	}

	var subRanges []blockstore.CatchupRange
	current := r.Start

	for current <= r.End {
		chunkEnd := min(current+MAX_RANGE_SIZE-1, r.End)
		chunkCurrent := current - 1
		if current <= r.Current {
			chunkCurrent = min(r.Current, chunkEnd)
		}

		subRanges = append(subRanges, blockstore.CatchupRange{
			Start:   current,
			End:     chunkEnd,
			Current: chunkCurrent,
		})
		current = chunkEnd + 1
	}

	cw.logger.Info("Split large catchup range",
		"chain", cw.chain.GetName(),
		"original_range", fmt.Sprintf("%d-%d", r.Start, r.End),
		"original_size", rangeSize,
		"sub_ranges", len(subRanges),
	)

	return subRanges
}

func (cw *CatchupWorker) processCatchupBlocksParallel() error {
	if len(cw.blockRanges) == 0 {
		cw.logger.Info("No catchup ranges to process")
		return nil
	}

	// Process multiple ranges in parallel
	var wg sync.WaitGroup
	rangeChan := make(chan blockstore.CatchupRange, len(cw.blockRanges))

	// Fill channel with ranges
	for _, r := range cw.blockRanges {
		rangeChan <- r
	}
	close(rangeChan)

	// Start parallel workers
	for i := 0; i < CATCHUP_WORKERS; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			cw.logger.Debug("Starting catchup worker", "worker_id", workerID)

			for r := range rangeChan {
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
	_ = cw.blockStore.SaveCatchupProgress(cw.chain.GetName(), r.Start, r.End, current)
}

func (cw *CatchupWorker) completeRange(r blockstore.CatchupRange) error {
	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()

	cw.logger.Info("Completing catchup range",
		"chain", cw.chain.GetName(),
		"range", fmt.Sprintf("%d-%d", r.Start, r.End),
	)

	_ = cw.blockStore.DeleteCatchupRange(cw.chain.GetName(), r.Start, r.End)

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

	for _, r := range cw.blockRanges {
		current := min(r.Current, r.End)
		cw.logger.Debug("Saving final catchup progress on close",
			"range", fmt.Sprintf("%d-%d", r.Start, r.End),
			"current", current,
		)
		if err := cw.blockStore.SaveCatchupProgress(cw.chain.GetName(), r.Start, r.End, current); err != nil {
			cw.logger.Error("Failed to save progress on close",
				"range", fmt.Sprintf("%d-%d", r.Start, r.End),
				"error", err,
			)
		}
	}

	return nil
}

func mergeRanges(ranges []blockstore.CatchupRange) []blockstore.CatchupRange {
	if len(ranges) == 0 {
		return ranges
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	merged := []blockstore.CatchupRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}
