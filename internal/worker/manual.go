package worker

import (
	"context"
	"time"

	"github.com/fystack/multichain-indexer/pkg/store/missingblockstore"
)

// ManualConfig controls the manual worker loop behavior.
type ManualConfig struct {
	MaxEmptyAttempts  int
	EmptySleep        time.Duration
	DelayPerIteration time.Duration
}

var DefaultManualConfig = ManualConfig{
	MaxEmptyAttempts: 3,
	EmptySleep:       3 * time.Second,
}

type ManualWorker struct {
	*BaseWorker
	config ManualConfig
	mbs    missingblockstore.MissingBlocksStore
}

func NewManualWorker(ctx context.Context, deps WorkerDeps) *ManualWorker {
	return &ManualWorker{
		BaseWorker: newWorkerWithMode(ctx, deps, ModeManual),
		mbs:        missingblockstore.NewMissingBlocksStore(deps.Redis),
		config:     DefaultManualConfig,
	}
}

func (mw *ManualWorker) Start() {
	mw.logger.Info("Starting manual worker", "chain", mw.deps.Chain.GetName())

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-mw.ctx.Done():
				return
			case <-ticker.C:
				mw.logMissingRangesMetric()
			}
		}
	}()

	go mw.loop()
}

func (mw *ManualWorker) loop() {
	ctx := mw.ctx
	emptyAttempts := 0

	for {
		select {
		case <-ctx.Done():
			mw.logger.Info("Manual worker stopped", "chain", mw.deps.Chain.GetName())
			return
		default:
		}

		start, end, err := mw.mbs.GetNextRange(ctx, mw.deps.Chain.GetNetworkInternalCode())
		if err != nil {
			mw.logger.Error("GetNextRange failed", "err", err, "chain", mw.deps.Chain.GetName())
			time.Sleep(time.Second)
			continue
		}

		if start == 0 && end == 0 {
			emptyAttempts++
			count, _ := mw.mbs.CountRanges(ctx, mw.deps.Chain.GetNetworkInternalCode())
			if emptyAttempts >= mw.config.MaxEmptyAttempts {
				mw.logger.Info("No ranges to process, sleeping",
					"chain", mw.deps.Chain.GetName(),
					"sleep", mw.config.EmptySleep,
					"queued_ranges", count,
				)
				time.Sleep(mw.config.EmptySleep)
				emptyAttempts = 0
			} else {
				time.Sleep(time.Second)
			}
			continue
		}

		emptyAttempts = 0
		mw.handleRange(ctx, start, end)
		if mw.config.DelayPerIteration > 0 {
			time.Sleep(mw.config.DelayPerIteration)
		}
	}
}

func (mw *ManualWorker) handleRange(ctx context.Context, start, end uint64) {
	mw.logger.Info("Processing manual range",
		"chain", mw.deps.Chain.GetName(),
		"start", start,
		"end", end,
	)

	results, err := mw.deps.Chain.GetBlocks(ctx, start, end, false)
	if err != nil {
		mw.logger.Error("GetBlocks failed", "err", err, "chain", mw.deps.Chain.GetName())
		time.Sleep(time.Second)
		return
	}

	lastSuccess := mw.processBatchResults(start, results, false)

	mw.logger.Info("Finished manual range",
		"chain", mw.deps.Chain.GetName(),
		"start", start,
		"end", end,
		"last_success", lastSuccess,
	)

	if lastSuccess >= start {
		_ = mw.mbs.SetRangeProcessed(ctx, mw.deps.Chain.GetNetworkInternalCode(), start, end, lastSuccess)
	}
	if lastSuccess >= end {
		if err := mw.mbs.RemoveRange(ctx, mw.deps.Chain.GetNetworkInternalCode(), start, end); err != nil {
			mw.logger.Error("RemoveRange failed", "err", err, "chain", mw.deps.Chain.GetName())
		}
	}
}

func (mw *ManualWorker) logMissingRangesMetric() {
	ranges, err := mw.mbs.ListRanges(mw.ctx, mw.deps.Chain.GetNetworkInternalCode())
	if err != nil {
		mw.logger.Warn("ListRanges failed", "chain", mw.deps.Chain.GetName(), "err", err)
		return
	}

	rangeCount := len(ranges)
	status := "Red flag"
	switch {
	case rangeCount <= 2:
		status = "Healthy"
	case rangeCount <= 10:
		status = "Warning"
	}

	mw.logger.Info("Missing block ranges status",
		"chain", mw.deps.Chain.GetName(),
		"status", status,
		"missing_count", rangeCount,
	)
}
