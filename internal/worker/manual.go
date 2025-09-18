package worker

import (
	"context"
	"time"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/missingblockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
)

// ManualConfig controls the manual worker loop behavior
type ManualConfig struct {
	MaxEmptyAttempts  int           // max consecutive empty checks before sleeping
	EmptySleep        time.Duration // sleep duration when idle
	DelayPerIteration time.Duration // delay after processing each range
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

func NewManualWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	redisClient infra.RedisClient,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *ManualWorker {
	return &ManualWorker{
		BaseWorker: newWorkerWithMode(
			ctx,
			chain,
			cfg,
			kv,
			blockStore,
			emitter,
			pubkeyStore,
			ModeManual,
			failedChan,
		),
		mbs:    missingblockstore.NewMissingBlocksStore(redisClient),
		config: DefaultManualConfig,
	}
}

func (mw *ManualWorker) Start() {
	mw.logger.Info("Starting manual worker", "chain", mw.chain.GetName())

	// Periodic metrics
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
			mw.logger.Info("Manual worker stopped", "chain", mw.chain.GetName())
			return
		default:
		}

		start, end, err := mw.mbs.GetNextRange(ctx, mw.chain.GetNetworkInternalCode())
		if err != nil {
			mw.logger.Error("GetNextRange failed", "err", err, "chain", mw.chain.GetName())
			time.Sleep(time.Second)
			continue
		}
		if start == 0 && end == 0 {
			emptyAttempts++
			count, _ := mw.mbs.CountRanges(ctx, mw.chain.GetNetworkInternalCode())
			if emptyAttempts >= mw.config.MaxEmptyAttempts {
				mw.logger.Info("No ranges to process, sleeping",
					"chain", mw.chain.GetName(),
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
	mw.logger.Info("Processing range",
		"chain", mw.chain.GetName(),
		"start", start,
		"end", end,
	)

	results, err := mw.chain.GetBlocks(ctx, start, end, false)
	if err != nil {
		mw.logger.Error("GetBlocks failed", "err", err, "chain", mw.chain.GetName())
		time.Sleep(time.Second)
		return
	}

	lastSuccess := start - 1
	for _, res := range results {
		if mw.handleBlockResult(res) {
			lastSuccess = res.Number
		}
	}

	mw.logger.Info("Finished processing",
		"chain", mw.chain.GetName(),
		"start", start,
		"end", end,
		"lastSuccess", lastSuccess,
	)

	if lastSuccess >= start {
		_ = mw.mbs.SetRangeProcessed(ctx, mw.chain.GetNetworkInternalCode(), start, end, lastSuccess)
	}
	if lastSuccess >= end {
		if err := mw.mbs.RemoveRange(ctx, mw.chain.GetNetworkInternalCode(), start, end); err != nil {
			mw.logger.Error("RemoveRange failed", "err", err, "chain", mw.chain.GetName())
		}
	}
}

func (mw *ManualWorker) logMissingRangesMetric() {
	ranges, err := mw.mbs.ListRanges(mw.ctx, mw.chain.GetNetworkInternalCode())
	if err != nil {
		mw.logger.Warn("ListRanges failed", "chain", mw.chain.GetName(), "err", err)
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
		"chain", mw.chain.GetName(),
		"status", status,
		"missing_count", rangeCount,
	)
}
