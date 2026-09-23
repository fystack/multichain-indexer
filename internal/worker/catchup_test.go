package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/stretchr/testify/require"
)

// fakeCatchupStore is an in-memory catchupstore.Store for a single chain, with
// deletion tracking so tests can assert a range was completed.
type fakeCatchupStore struct {
	mu      sync.Mutex
	current map[string]uint64 // "start-end" -> current
	deleted map[string]bool
}

func newFakeCatchupStore() *fakeCatchupStore {
	return &fakeCatchupStore{current: map[string]uint64{}, deleted: map[string]bool{}}
}

func fakeCatchupField(start, end uint64) string {
	return fmt.Sprintf("%d-%d", start, end)
}

func (f *fakeCatchupStore) SaveRanges(
	_ context.Context,
	_ string,
	ranges []blockstore.CatchupRange,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range ranges {
		if r.Start == 0 || r.End < r.Start {
			continue
		}
		f.current[fakeCatchupField(r.Start, r.End)] = r.Current
	}
	return nil
}

func (f *fakeCatchupStore) SaveProgress(
	_ context.Context,
	_ string,
	start, end, current uint64,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current[fakeCatchupField(start, end)] = current
	return nil
}

func (f *fakeCatchupStore) GetProgress(
	_ context.Context,
	_ string,
) ([]blockstore.CatchupRange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ranges := make([]blockstore.CatchupRange, 0, len(f.current))
	for field, current := range f.current {
		var start, end uint64
		if _, err := fmt.Sscanf(field, "%d-%d", &start, &end); err != nil {
			continue
		}
		ranges = append(ranges, blockstore.CatchupRange{Start: start, End: end, Current: current})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	return ranges, nil
}

func (f *fakeCatchupStore) DeleteRange(
	_ context.Context,
	_ string,
	start, end uint64,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.current, fakeCatchupField(start, end))
	f.deleted[fakeCatchupField(start, end)] = true
	return nil
}

func (f *fakeCatchupStore) hasDeleted(start, end uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleted[fakeCatchupField(start, end)]
}

// newTestCatchupWorker builds a CatchupWorker wired to the given stubs with a
// short idle interval so the keep-alive loop can be exercised quickly.
func newTestCatchupWorker(
	ctx context.Context,
	chain *stubIndexer,
	store *stubBlockStore,
	catchupStore *fakeCatchupStore,
) *CatchupWorker {
	cfg := testChainConfig()
	return &CatchupWorker{
		BaseWorker: &BaseWorker{
			ctx:          ctx,
			cancel:       func() {},
			logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			config:       cfg,
			chain:        chain,
			blockStore:   store,
			catchupStore: catchupStore,
			emitter:      &captureEmitter{},
			failedChan:   make(chan FailedBlockEvent, 8),
		},
		workerPool:   make(chan struct{}, CATCHUP_WORKERS),
		idleInterval: 5 * time.Millisecond,
	}
}

// allBlocksOK returns every requested block as a successful result.
func allBlocksOK(_ context.Context, from, to uint64, _ bool) ([]indexer.BlockResult, error) {
	var out []indexer.BlockResult
	for n := from; n <= to; n++ {
		out = append(out, indexer.BlockResult{
			Number: n,
			Block:  &types.Block{Number: n, Hash: "0x", ParentHash: "0x"},
		})
	}
	return out, nil
}

// TestCatchupWorkerPicksUpRangesQueuedAfterDrain reproduces the BSC bug: the
// catchup loop used to exit permanently once all ranges drained, so ranges the
// regular worker queued later (via lag skip-ahead) were never processed and
// piled up in the store. The keep-alive loop must pick them up.
func TestCatchupWorkerPicksUpRangesQueuedAfterDrain(t *testing.T) {
	t.Parallel()

	chain := &stubIndexer{
		name:          "bsc",
		internalCode:  "BSC_MAINNET",
		networkType:   enum.NetworkTypeEVM,
		latest:        1000,
		getBlocksFunc: allBlocksOK,
	}
	// Start with no ranges: the loop immediately drains and goes idle.
	store := &stubBlockStore{latestBlock: 1000}
	catchupStore := newFakeCatchupStore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cw := newTestCatchupWorker(ctx, chain, store, catchupStore)

	done := make(chan struct{})
	go func() {
		cw.runCatchup()
		close(done)
	}()

	// Simulate the regular worker queuing a catchup range at runtime, after the
	// catchup loop has already drained and gone idle.
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, catchupStore.SaveProgress(ctx, "BSC_MAINNET", 100, 105, 99))

	// The idle loop should pick it up and complete (delete) it.
	require.Eventually(t, func() bool {
		return catchupStore.hasDeleted(100, 105)
	}, time.Second, 5*time.Millisecond, "queued range was not processed by the keep-alive loop")

	// The loop must still be running (did not exit on drain).
	select {
	case <-done:
		t.Fatal("runCatchup exited instead of staying alive for new ranges")
	default:
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runCatchup did not stop after context cancel")
	}
}
