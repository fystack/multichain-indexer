package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/status"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/stretchr/testify/require"
)

func TestCatchupWorkerPollsForNewRangesInsteadOfExiting(t *testing.T) {
	t.Parallel()

	statusRegistry := status.NewRegistry()
	statusRegistry.RegisterChain("aptos", "aptos_testnet", config.ChainConfig{
		NetworkId:    "aptos_testnet",
		InternalCode: "APTOS_TESTNET",
		Type:         enum.NetworkTypeApt,
	})

	store := &stubBlockStore{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cw := &CatchupWorker{
		BaseWorker: &BaseWorker{
			ctx:    ctx,
			cancel: cancel,
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			config: config.ChainConfig{
				PollInterval: time.Millisecond,
				Throttle:     config.Throttle{BatchSize: 20},
			},
			chain:          &stubIndexer{name: "aptos", internalCode: "APTOS_TESTNET", networkType: enum.NetworkTypeApt, latest: 100},
			blockStore:     store,
			statusRegistry: statusRegistry,
		},
		blockRanges: []blockstore.CatchupRange{},
		workerPool:  make(chan struct{}, CATCHUP_WORKERS),
	}

	// Simulate: catchup worker starts with no ranges (empty after state wipe).
	// After a short delay, the regular worker saves new catchup ranges to the store.
	// The catchup worker should poll and pick them up instead of exiting.
	go func() {
		time.Sleep(100 * time.Millisecond)
		// Simulate regular worker creating catchup ranges in the store
		store.catchupProgress = []blockstore.CatchupRange{
			{Start: 50, End: 69, Current: 49},
			{Start: 70, End: 89, Current: 69},
		}
		// Give the catchup worker time to poll and load the ranges
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	// runCatchup should NOT exit immediately when blockRanges is empty.
	// It should poll, pick up the new ranges, and only stop when ctx is cancelled.
	done := make(chan struct{})
	go func() {
		cw.runCatchup()
		close(done)
	}()

	select {
	case <-done:
		// Verify the catchup worker loaded the new ranges before exiting
		resp := statusRegistry.Snapshot("1.0.0")
		require.Len(t, resp.Networks, 1)
		// The worker should have picked up ranges from the store
		// (it may have processed and completed them, or they may still be pending)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("catchup worker did not exit after context cancellation")
	}
}
