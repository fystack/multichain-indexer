package worker

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/events"
)

func TestRescannerSyncSkipsBlocksPendingRemoval(t *testing.T) {
	initTestLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chain := &stubIndexer{name: "chain", internalCode: "c", networkType: enum.NetworkTypeSol}
	store := &stubBlockStore{failedBlocks: []uint64{5, 6}}
	rw := NewRescannerWorker(ctx, chain, testChainConfig(), noopKVStore{}, store, nil, events.Emitter(nil), nil, make(chan FailedBlockEvent, 1), nil)

	rw.removeBlocks([]uint64{5})
	if err := rw.syncFromKV(); err != nil {
		t.Fatalf("syncFromKV: %v", err)
	}

	got := rw.collectBlocksForRetry()
	if len(got) != 1 || got[0] != 6 {
		t.Fatalf("expected only block 6 to be retried while 5 awaits removal, got %v", got)
	}
}
