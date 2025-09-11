package worker

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
)

type BlockRange struct {
	Start, End uint64
}

type CatchupWorker struct {
	*BaseWorker
	blockRanges []BlockRange
}

func composeCatchupKey(chain string) string {
	return fmt.Sprintf("%s/%s/", chain, constant.KVPrefixProgressCatchup)
}
func catchupKey(chain string, start, end uint64) string {
	return fmt.Sprintf("%s/%s/%d-%d", chain, constant.KVPrefixProgressCatchup, start, end)
}

func NewCatchupWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore *blockstore.Store,
	emitter *events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *CatchupWorker {
	worker := newWorkerWithMode(ctx, chain, cfg, kv, blockStore, emitter, pubkeyStore, ModeCatchup, failedChan)
	cw := &CatchupWorker{BaseWorker: worker}
	cw.blockRanges = cw.loadCatchupProgress()
	return cw
}

func (cw *CatchupWorker) Start() {
	cw.logger.Info("Starting catchup worker",
		"chain", cw.chain.GetName(),
		"ranges", cw.blockRanges,
	)
	go cw.run(cw.processCatchupBlocks)
}

func (cw *CatchupWorker) loadCatchupProgress() []BlockRange {
	var ranges []BlockRange

	// 1. Existing progress
	if pairs, err := cw.kvstore.List(composeCatchupKey(cw.chain.GetName())); err == nil {
		for _, p := range pairs {
			if s, e := extractRangeFromKey(p.Key); s > 0 {
				ranges = append(ranges, BlockRange{Start: s, End: e})
			}
		}
	}

	// 2. If nothing queued, enqueue fresh catchup
	if len(ranges) == 0 {
		if latest, err1 := cw.blockStore.GetLatestBlock(cw.chain.GetName()); err1 == nil {
			if head, err2 := cw.chain.GetLatestBlockNumber(cw.ctx); err2 == nil && head > latest {
				start, end := latest+1, head
				_ = cw.kvstore.Set(catchupKey(cw.chain.GetName(), start, end), strconv.FormatUint(start-1, 10))
				ranges = append(ranges, BlockRange{Start: start, End: end})
				cw.logger.Info("Queued fresh catchup range",
					"chain", cw.chain.GetName(),
					"start", start, "end", end,
				)
			}
		}
	}

	return mergeRanges(ranges)
}

func (cw *CatchupWorker) processCatchupBlocks() error {
	if len(cw.blockRanges) == 0 {
		return nil // idle
	}

	r := cw.blockRanges[0]
	pKey := catchupKey(cw.chain.GetName(), r.Start, r.End)
	progressStr, _ := cw.kvstore.Get(pKey)
	current := parseProgress(r.Start, r.End, progressStr)

	if current > r.End {
		return cw.completeRange(r, pKey)
	}

	// Process batch [current, min(current+batch-1, r.End)]
	end := min(current+uint64(cw.config.BatchSize)-1, r.End)
	startTime := time.Now()
	results, err := cw.chain.GetBlocks(cw.ctx, current, end)
	if err != nil {
		return fmt.Errorf("get catchup blocks: %w", err)
	}

	lastSuccess := current - 1
	for _, res := range results {
		if cw.handleBlockResult(res) && res.Number > lastSuccess {
			lastSuccess = res.Number
		}
	}

	elapsed := time.Since(startTime)
	cw.logger.Info("Catchup processed batch",
		"chain", cw.chain.GetName(),
		"range_start", r.Start, "range_end", r.End,
		"start", current, "end", end,
		"elapsed", elapsed,
		"last_success", lastSuccess,
		"expected", end-current+1,
		"got", len(results),
	)

	// persist progress
	if lastSuccess >= current {
		_ = cw.kvstore.Set(pKey, strconv.FormatUint(min(lastSuccess, r.End), 10))
	}

	// Progress log
	processed := uint64(0)
	if lastSuccess >= r.Start {
		processed = (lastSuccess - r.Start) + 1
	}
	cw.logger.Info("Catchup progress",
		"chain", cw.chain.GetName(),
		"range_start", r.Start, "range_end", r.End,
		"processed", processed, "total", r.End-r.Start+1,
	)

	// Finished?
	if lastSuccess >= r.End {
		return cw.completeRange(r, pKey)
	}
	return nil
}

func (cw *CatchupWorker) completeRange(r BlockRange, key string) error {
	_ = cw.kvstore.Delete(key)
	cw.blockRanges = cw.blockRanges[1:]
	cw.logger.Info("Catchup range completed",
		"chain", cw.chain.GetName(),
		"range_start", r.Start, "range_end", r.End,
	)
	return nil
}

func parseProgress(start, end uint64, val string) uint64 {
	if val == "" {
		return start
	}
	if p, err := strconv.ParseUint(val, 10, 64); err == nil {
		switch {
		case p < start:
			return start
		case p >= end:
			return end
		default:
			return p + 1
		}
	}
	return start
}

func mergeRanges(ranges []BlockRange) []BlockRange {
	if len(ranges) == 0 {
		return ranges
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	merged := []BlockRange{ranges[0]}
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

func extractRangeFromKey(key string) (uint64, uint64) {
	// <chain>/catchup/<start>-<end>
	parts := strings.Split(key, "/")
	if len(parts) < 4 {
		return 0, 0
	}
	se := strings.Split(parts[len(parts)-1], "-")
	if len(se) != 2 {
		return 0, 0
	}
	s, err1 := strconv.ParseUint(se[0], 10, 64)
	e, err2 := strconv.ParseUint(se[1], 10, 64)
	if err1 == nil && err2 == nil && s <= e {
		return s, e
	}
	return 0, 0
}
