package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
)

type CatchupWorker struct {
	*BaseWorker
	blockRanges []blockstore.CatchupRange
	progressMu  sync.Mutex
}

func NewCatchupWorker(ctx context.Context, deps WorkerDeps) *CatchupWorker {
	worker := newWorkerWithMode(ctx, deps, ModeCatchup)
	cw := &CatchupWorker{BaseWorker: worker}
	cw.blockRanges = cw.loadCatchupProgress()
	return cw
}

func (cw *CatchupWorker) Start() {
	totalBlocks := uint64(0)
	for _, r := range cw.blockRanges {
		totalBlocks += r.End - r.Start + 1
	}

	cw.logger.Info("Starting catchup worker",
		"chain", cw.deps.Chain.GetName(),
		"ranges", len(cw.blockRanges),
		"total_blocks", totalBlocks,
		"parallel_workers", defaultCatchupWorkerCount,
	)
	go cw.run(cw.processCatchupBlocksParallel)
}

func (cw *CatchupWorker) loadCatchupProgress() []blockstore.CatchupRange {
	if progress, err := cw.deps.BlockStore.GetCatchupProgress(cw.deps.Chain.GetNetworkInternalCode()); err == nil {
		cw.logger.Info("Loaded catchup progress",
			"chain", cw.deps.Chain.GetName(),
			"ranges", len(progress),
		)
		if len(progress) > 0 {
			return progress
		}
	} else {
		cw.logger.Warn("Failed to load catchup progress", "chain", cw.deps.Chain.GetName(), "error", err)
	}

	return cw.bootstrapCatchupRanges()
}

func (cw *CatchupWorker) splitLargeRange(r blockstore.CatchupRange) []blockstore.CatchupRange {
	subRanges := splitCatchupRange(r, defaultCatchupRangeSize)
	if len(subRanges) > 1 {
		cw.logger.Info("Split large catchup range",
			"chain", cw.deps.Chain.GetName(),
			"original_range", formatRange(r.Start, r.End),
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

	var wg sync.WaitGroup
	rangeChan := make(chan blockstore.CatchupRange, len(cw.blockRanges))
	for _, r := range cw.blockRanges {
		rangeChan <- r
	}
	close(rangeChan)

	for i := 0; i < defaultCatchupWorkerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for r := range rangeChan {
				if err := cw.processRange(r, workerID); err != nil {
					cw.logger.Error("Failed to process range",
						"worker_id", workerID,
						"range", formatRange(r.Start, r.End),
						"error", err,
					)
				}
			}
		}(i)
	}

	wg.Wait()
	cw.logger.Info("Catchup processing completed", "chain", cw.deps.Chain.GetName())
	cw.blockRanges = cw.loadCatchupProgress()
	return nil
}

func (cw *CatchupWorker) processRange(r blockstore.CatchupRange, workerID int) error {
	batchCount := 0
	startTime := time.Now()

	cw.logger.Info("Processing catchup range",
		"worker_id", workerID,
		"range", formatRange(r.Start, r.End),
		"size", r.End-r.Start+1,
	)

	current := r.Current + 1
	if current > r.End {
		return cw.completeRange(r)
	}

	batchSize := uint64(cw.deps.Config.Throttle.BatchSize)
	if batchSize == 0 {
		batchSize = 1
	}
	lastSuccess := current - 1

	for current <= r.End {
		select {
		case <-cw.ctx.Done():
			cw.logger.Info("Context cancelled; saving catchup progress",
				"worker_id", workerID,
				"range", formatRange(r.Start, r.End),
				"current", lastSuccess,
			)
			cw.saveProgress(r, min(lastSuccess, r.End))
			return cw.ctx.Err()
		default:
		}

		end := min(current+batchSize-1, r.End)
		results, err := cw.deps.Chain.GetBlocks(cw.ctx, current, end, true)
		if err != nil {
			cw.logger.Warn("Failed to get blocks, retrying",
				"worker_id", workerID,
				"range", formatRange(current, end),
				"error", err,
			)
			time.Sleep(time.Second)
			continue
		}

		batchSuccess := cw.processBatchResults(current, results, true)
		if batchSuccess >= current {
			lastSuccess = batchSuccess
			current = batchSuccess + 1
		} else {
			current = end + 1
		}

		batchCount++
		if batchCount%catchupProgressSaveEvery == 0 {
			cw.saveProgress(r, min(lastSuccess, r.End))
			processed := lastSuccess - r.Start + 1
			total := r.End - r.Start + 1
			progress := float64(processed) / float64(total) * 100

			cw.logger.Info("Catchup progress",
				"worker_id", workerID,
				"range", formatRange(r.Start, r.End),
				"current", lastSuccess,
				"progress", fmt.Sprintf("%.1f%%", progress),
				"processed", processed,
				"total", total,
				"elapsed", time.Since(startTime).Truncate(time.Second),
				"batches", batchCount,
			)
		}
	}

	cw.saveProgress(r, min(lastSuccess, r.End))
	cw.logger.Info("Catchup range completed",
		"worker_id", workerID,
		"range", formatRange(r.Start, r.End),
		"blocks_processed", r.End-r.Start+1,
		"elapsed", time.Since(startTime).Truncate(time.Second),
		"batches", batchCount,
	)

	return cw.completeRange(r)
}

func (cw *CatchupWorker) saveProgress(r blockstore.CatchupRange, current uint64) {
	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()
	_ = cw.deps.BlockStore.SaveCatchupProgress(cw.deps.Chain.GetNetworkInternalCode(), r.Start, r.End, current)
}

func (cw *CatchupWorker) completeRange(r blockstore.CatchupRange) error {
	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()

	_ = cw.deps.BlockStore.DeleteCatchupRange(cw.deps.Chain.GetNetworkInternalCode(), r.Start, r.End)
	for i, existing := range cw.blockRanges {
		if existing.Start == r.Start && existing.End == r.End {
			cw.blockRanges = append(cw.blockRanges[:i], cw.blockRanges[i+1:]...)
			break
		}
	}
	return nil
}

func (cw *CatchupWorker) Close() error {
	cw.progressMu.Lock()
	defer cw.progressMu.Unlock()

	for _, r := range cw.blockRanges {
		current := min(r.Current, r.End)
		if err := cw.deps.BlockStore.SaveCatchupProgress(cw.deps.Chain.GetNetworkInternalCode(), r.Start, r.End, current); err != nil {
			cw.logger.Error("Failed to save catchup progress on close",
				"range", formatRange(r.Start, r.End),
				"error", err,
			)
		}
	}

	return nil
}

func (cw *CatchupWorker) bootstrapCatchupRanges() []blockstore.CatchupRange {
	latest, err := cw.deps.BlockStore.GetLatestBlock(cw.deps.Chain.GetNetworkInternalCode())
	if err != nil {
		return nil
	}

	head, err := cw.deps.Chain.GetLatestBlockNumber(cw.ctx)
	if err != nil || head <= latest {
		return nil
	}

	start, end := latest+1, head
	cw.logger.Info("Creating catchup ranges",
		"chain", cw.deps.Chain.GetName(),
		"latest_block", latest,
		"head_block", head,
		"catchup_start", start,
		"catchup_end", end,
	)

	ranges := cw.splitLargeRange(blockstore.CatchupRange{Start: start, End: end, Current: start - 1})
	cw.persistRanges(ranges)
	return ranges
}

func (cw *CatchupWorker) persistRanges(ranges []blockstore.CatchupRange) {
	for _, r := range ranges {
		if err := cw.deps.BlockStore.SaveCatchupProgress(cw.deps.Chain.GetNetworkInternalCode(), r.Start, r.End, r.Current); err != nil {
			cw.logger.Error("Failed to save catchup range",
				"chain", cw.deps.Chain.GetName(),
				"range", formatRange(r.Start, r.End),
				"error", err,
			)
		}
	}
}
