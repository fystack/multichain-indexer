package indexer

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/infra"
)

type BlockRange struct {
	Start uint64
	End   uint64
}

type CatchupWorker struct {
	*BaseWorker
	blockRanges []BlockRange
}

// progress key helpers (folder-structured)
func progressPrefix(chain string) string {
	return fmt.Sprintf("catchup/progress/%s/", chain)
}

func progressKey(chain string, start, end uint64) string {
	return fmt.Sprintf("catchup/progress/%s/%d-%d", chain, start, end)
}

// NewCatchupWorker creates a worker for historical range
func NewCatchupWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter) *CatchupWorker {
	worker := newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, addressBF, ModeCatchup)
	catchup := &CatchupWorker{BaseWorker: worker}
	catchup.blockRanges = catchup.loadCatchupProgress()
	return catchup
}

func (cw *CatchupWorker) Start() {
	cw.logger.Info("Starting catchup worker", "chain", cw.chain.GetName(), "block_ranges", cw.blockRanges)
	go cw.run(cw.processCatchupBlocks)
}

// loadCatchupProgress reads pending progress ranges and failed blocks, then merges them
func (cw *CatchupWorker) loadCatchupProgress() []BlockRange {
	ranges := make([]BlockRange, 0)

	// 1) Existing progress ranges (folder style)
	if pairs, err := cw.kvstore.List(progressPrefix(cw.chain.GetName())); err == nil {
		for _, p := range pairs {
			key := p.Key // catchup/progress/<chain>/<start>-<end>
			parts := strings.Split(key, "/")
			if len(parts) < 4 {
				continue
			}
			rangePart := parts[len(parts)-1]
			se := strings.Split(rangePart, "-")
			if len(se) != 2 {
				continue
			}
			if s, err1 := strconv.ParseUint(se[0], 10, 64); err1 == nil {
				if e, err2 := strconv.ParseUint(se[1], 10, 64); err2 == nil && s <= e {
					ranges = append(ranges, BlockRange{Start: s, End: e})
				}
			}
		}
	}

	// 2) Failed blocks merged to ranges
	if blocks, err := cw.blockStore.GetFailedBlocks(cw.chain.GetName()); err == nil && len(blocks) > 0 {
		slices.Sort(blocks)
		start := blocks[0]
		prev := blocks[0]
		for i := 1; i < len(blocks); i++ {
			b := blocks[i]
			if b == prev || b == prev+1 {
				prev = b
				continue
			}
			ranges = append(ranges, BlockRange{Start: start, End: prev})
			start = b
			prev = b
		}
		ranges = append(ranges, BlockRange{Start: start, End: prev})
	}

	// If nothing queued yet, create a fresh catchup range from latest processed to chain head
	if len(ranges) == 0 {
		latestProcessed, err1 := cw.blockStore.GetLatestBlock(cw.chain.GetName())
		chainHead, err2 := cw.chain.GetLatestBlockNumber(cw.ctx)
		if err1 == nil && err2 == nil && chainHead > latestProcessed {
			start := latestProcessed + 1
			end := chainHead
			// persist progress as start-1
			_ = cw.kvstore.Set(progressKey(cw.chain.GetName(), start, end), strconv.FormatUint(start-1, 10))
			ranges = append(ranges, BlockRange{Start: start, End: end})
			cw.logger.Info("Queued fresh catchup range",
				"chain", cw.chain.GetName(),
				"range_start", start,
				"range_end", end,
			)
		}
	}

	// 3) Merge overlapping/adjacent ranges
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	merged := make([]BlockRange, 0, len(ranges))
	for _, r := range ranges {
		if len(merged) == 0 {
			merged = append(merged, r)
			continue
		}
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

func (cw *CatchupWorker) processCatchupBlocks() error {
	// Only process queued ranges; do not reload on each ticker
	if len(cw.blockRanges) == 0 {
		// idle; nothing to do
		return nil
	}

	// Take first range
	r := cw.blockRanges[0]
	// read per-range progress
	pKey := progressKey(cw.chain.GetName(), r.Start, r.End)
	progressStr, _ := cw.kvstore.Get(pKey)
	var current uint64
	if progressStr == "" {
		current = r.Start
	} else {
		p, _ := strconv.ParseUint(progressStr, 10, 64)
		if p < r.Start {
			current = r.Start
		} else if p > r.End {
			current = r.End
		} else {
			current = p + 1
		}
	}

	// Log pre-batch status
	total := r.End - r.Start + 1
	processed := uint64(0)
	if current > r.Start {
		processed = current - r.Start
	}
	cw.logger.Info("Catchup batch starting",
		"chain", cw.chain.GetName(),
		"range_start", r.Start,
		"range_end", r.End,
		"current", current,
		"processed", processed,
		"total", total,
	)

	if current > r.End {
		// range done; cleanup and move to next
		_ = cw.kvstore.Delete(pKey)
		cw.blockRanges = cw.blockRanges[1:]
		cw.logger.Info("Catchup range completed",
			"chain", cw.chain.GetName(),
			"range_start", r.Start,
			"range_end", r.End,
		)
		return nil
	}

	// process batch [current, min(current+batch-1, r.End)]
	end := min(current+uint64(cw.config.BatchSize)-1, r.End)
	startTime := time.Now()
	results, err := cw.chain.GetBlocks(cw.ctx, current, end)
	if err != nil {
		return fmt.Errorf("get catchup blocks: %w", err)
	}

	lastSuccess := current - 1
	for _, res := range results {
		if cw.handleBlockResult(res) {
			if res.Number > lastSuccess {
				lastSuccess = res.Number
			}
		}
	}

	elapsed := time.Since(startTime)
	cw.logger.Info("Processing catchup blocks",
		"chain", cw.chain.GetName(),
		"start", current,
		"end", end,
		"elapsed", elapsed,
		"last_success", lastSuccess,
		"expected number of blocks", end-current+1,
		"actual blocks", len(results),
	)

	// persist progress and advance
	if lastSuccess >= current {
		_ = cw.kvstore.Set(pKey, strconv.FormatUint(lastSuccess, 10))
	}

	// Progress log
	newProcessed := (lastSuccess - r.Start) + 1
	if lastSuccess < r.Start {
		newProcessed = 0
	}
	cw.logger.Info("Catchup progress",
		"chain", cw.chain.GetName(),
		"range_start", r.Start,
		"range_end", r.End,
		"processed", newProcessed,
		"total", total,
	)

	// if finished range, cleanup
	if lastSuccess >= r.End {
		_ = cw.kvstore.Delete(pKey)
		cw.logger.Info("Deleting progress key", "key", pKey)
		cw.blockRanges = cw.blockRanges[1:]
		cw.logger.Info("Catchup range completed",
			"chain", cw.chain.GetName(),
			"range_start", r.Start,
			"range_end", r.End,
		)
	}
	return nil
}
