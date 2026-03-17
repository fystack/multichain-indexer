package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fystack/multichain-indexer/internal/worker"
)

// ChainTracker records block statuses for a single chain.
type ChainTracker struct {
	mu     sync.Mutex
	blocks map[uint64]worker.BlockStatus
}

// Tracker is a thread-safe per-chain block status recorder.
type Tracker struct {
	mu     sync.RWMutex
	chains map[string]*ChainTracker
}

// NewTracker creates a new Tracker.
func NewTracker() *Tracker {
	return &Tracker{
		chains: make(map[string]*ChainTracker),
	}
}

// Observe records a block status. Safe for concurrent use from multiple goroutines.
func (t *Tracker) Observe(chainName string, blockNumber uint64, status worker.BlockStatus) {
	t.mu.RLock()
	ct, ok := t.chains[chainName]
	t.mu.RUnlock()

	if !ok {
		t.mu.Lock()
		// Double-check after acquiring write lock
		ct, ok = t.chains[chainName]
		if !ok {
			ct = &ChainTracker{blocks: make(map[uint64]worker.BlockStatus)}
			t.chains[chainName] = ct
		}
		t.mu.Unlock()
	}

	ct.mu.Lock()
	ct.blocks[blockNumber] = status
	ct.mu.Unlock()
}

// ChainReport holds the completeness report for a single chain.
type ChainReport struct {
	Chain     string
	Start     uint64
	End       uint64
	Expected  uint64
	Processed uint64
	NotFound  uint64
	Failed    uint64
	Missing   uint64

	FailedBlocks  []uint64
	MissingBlocks []uint64
}

// Pass returns true if all blocks are accounted for (no missing or failed).
// NotFound is acceptable (e.g. Solana skipped slots).
func (r *ChainReport) Pass() bool {
	return r.Missing == 0 && r.Failed == 0
}

// Report generates a completeness report for a chain over [start, end].
func (t *Tracker) Report(chain string, start, end uint64) ChainReport {
	report := ChainReport{
		Chain:    chain,
		Start:    start,
		End:      end,
		Expected: end - start + 1,
	}

	t.mu.RLock()
	ct, ok := t.chains[chain]
	t.mu.RUnlock()

	if !ok {
		report.Missing = report.Expected
		for b := start; b <= end; b++ {
			report.MissingBlocks = append(report.MissingBlocks, b)
		}
		return report
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	for b := start; b <= end; b++ {
		status, exists := ct.blocks[b]
		if !exists {
			report.Missing++
			report.MissingBlocks = append(report.MissingBlocks, b)
			continue
		}
		switch status {
		case worker.BlockStatusProcessed:
			report.Processed++
		case worker.BlockStatusNotFound:
			report.NotFound++
		case worker.BlockStatusFailed:
			report.Failed++
			report.FailedBlocks = append(report.FailedBlocks, b)
		}
	}

	return report
}

// PrintReport prints a formatted completeness report for the given chain.
func PrintReport(r ChainReport) {
	fmt.Printf("\n=== %s ===\n", r.Chain)
	fmt.Printf("  Range:        %d - %d\n", r.Start, r.End)
	fmt.Printf("  Expected:     %d\n", r.Expected)
	fmt.Printf("  Processed:    %d\n", r.Processed)
	fmt.Printf("  Not Found:    %d\n", r.NotFound)
	fmt.Printf("  Failed:       %d\n", r.Failed)
	fmt.Printf("  Missing:      %d\n", r.Missing)

	if r.Pass() {
		extra := ""
		if r.NotFound > 0 {
			extra = fmt.Sprintf(" (%d not found, e.g. skipped slots)", r.NotFound)
		}
		fmt.Printf("  Status:       PASS — all blocks accounted for%s\n", extra)
	} else {
		unaccounted := r.Missing + r.Failed
		fmt.Printf("  Status:       FAIL — %d block(s) unaccounted\n", unaccounted)
	}

	if len(r.FailedBlocks) > 0 {
		sort.Slice(r.FailedBlocks, func(i, j int) bool { return r.FailedBlocks[i] < r.FailedBlocks[j] })
		fmt.Printf("\n  Failed blocks:  %s\n", compressRanges(r.FailedBlocks))
	}
	if len(r.MissingBlocks) > 0 {
		sort.Slice(r.MissingBlocks, func(i, j int) bool { return r.MissingBlocks[i] < r.MissingBlocks[j] })
		fmt.Printf("  Missing blocks: %s\n", compressRanges(r.MissingBlocks))
	}

	fmt.Println()
}

// compressRanges formats a sorted slice of block numbers into compressed ranges.
// e.g., [1, 2, 3, 5, 7, 8] -> "1-3, 5, 7-8"
func compressRanges(blocks []uint64) string {
	if len(blocks) == 0 {
		return ""
	}

	var parts []string
	start := blocks[0]
	end := blocks[0]

	for i := 1; i < len(blocks); i++ {
		if blocks[i] == end+1 {
			end = blocks[i]
		} else {
			parts = append(parts, formatRange(start, end))
			start = blocks[i]
			end = blocks[i]
		}
	}
	parts = append(parts, formatRange(start, end))

	return strings.Join(parts, ", ")
}

func formatRange(start, end uint64) string {
	if start == end {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}
