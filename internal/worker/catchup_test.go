package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/require"
)

// newTestCatchupWorker builds a CatchupWorker wired to the given stubs with a
// short idle interval so the keep-alive loop can be exercised quickly.
func newTestCatchupWorker(
	ctx context.Context,
	chain *stubIndexer,
	store *stubBlockStore,
) *CatchupWorker {
	cfg := testChainConfig()
	return &CatchupWorker{
		BaseWorker: &BaseWorker{
			ctx:        ctx,
			cancel:     func() {},
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			config:     cfg,
			chain:      chain,
			blockStore: store,
			emitter:    &captureEmitter{},
			failedChan: make(chan FailedBlockEvent, 8),
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cw := newTestCatchupWorker(ctx, chain, store)

	done := make(chan struct{})
	go func() {
		cw.runCatchup()
		close(done)
	}()

	// Simulate the regular worker queuing a catchup range at runtime, after the
	// catchup loop has already drained and gone idle.
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, store.SaveCatchupProgress("BSC_MAINNET", 100, 105, 99))

	// The idle loop should pick it up and complete (delete) it.
	require.Eventually(t, func() bool {
		return store.hasDeleted(100, 105)
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
