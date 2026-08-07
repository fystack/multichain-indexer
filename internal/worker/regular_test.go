package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/status"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/stretchr/testify/require"
)

func TestRegularWorkerProcessRegularBlocksRecoversGapViaGetBlock(t *testing.T) {
	t.Parallel()

	chain := &stubIndexer{
		name:         "ethereum",
		internalCode: "eth",
		networkType:  enum.NetworkTypeEVM,
		latest:       102,
		getBlocksFunc: func(context.Context, uint64, uint64, bool) ([]indexer.BlockResult, error) {
			return []indexer.BlockResult{
				{
					Number: 100,
					Block: &types.Block{
						Number:     100,
						Hash:       "0x100",
						ParentHash: "0x099",
					},
				},
				{
					Number: 101,
					Error:  &indexer.Error{ErrorType: indexer.ErrorTypeUnknown, Message: "rpc timeout"},
				},
				{
					Number: 102,
					Block: &types.Block{
						Number:     102,
						Hash:       "0x102",
						ParentHash: "0x101",
					},
				},
			}, nil
		},
		getBlockFunc: func(_ context.Context, number uint64) (*types.Block, error) {
			switch number {
			case 101:
				return &types.Block{
					Number:     101,
					Hash:       "0x101",
					ParentHash: "0x100",
				}, nil
			case 102:
				return &types.Block{
					Number:     102,
					Hash:       "0x102",
					ParentHash: "0x101",
				}, nil
			default:
				return nil, errors.New("unexpected block")
			}
		},
	}
	store := &stubBlockStore{}
	rw := newTestRegularWorker(chain, store, 100, 3)

	err := rw.processRegularBlocks()
	require.NoError(t, err)
	require.Equal(t, uint64(103), rw.currentBlock)
	require.Equal(t, []uint64{102}, store.savedLatest)
	require.Empty(t, store.failedBlocks)
	require.Equal(t, []uint64{101, 102}, chain.getBlockCalls)
}

func TestRegularWorkerProcessRegularBlocksMarksUnresolvedGapFailed(t *testing.T) {
	t.Parallel()

	chain := &stubIndexer{
		name:         "ethereum",
		internalCode: "eth",
		networkType:  enum.NetworkTypeEVM,
		latest:       101,
		getBlocksFunc: func(context.Context, uint64, uint64, bool) ([]indexer.BlockResult, error) {
			return []indexer.BlockResult{
				{
					Number: 100,
					Error:  &indexer.Error{ErrorType: indexer.ErrorTypeUnknown, Message: "quota exceeded"},
				},
				{
					Number: 101,
					Block: &types.Block{
						Number:     101,
						Hash:       "0x101",
						ParentHash: "0x100",
					},
				},
			}, nil
		},
		getBlockFunc: func(context.Context, uint64) (*types.Block, error) {
			return nil, errors.New("429 too many requests")
		},
	}
	store := &stubBlockStore{}
	rw := newTestRegularWorker(chain, store, 100, 2)

	err := rw.processRegularBlocks()
	require.Error(t, err)
	require.Equal(t, uint64(100), rw.currentBlock)
	require.Empty(t, store.savedLatest)
	require.Equal(t, []uint64{100}, store.failedBlocks)
	require.Equal(t, []uint64{100, 100}, chain.getBlockCalls)
}

func TestBaseWorkerExecuteRecoverableConvertsPanicToError(t *testing.T) {
	t.Parallel()

	bw := &BaseWorker{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := bw.executeRecoverable("test panic", func() error {
		panic("boom")
	})
	require.Error(t, err)

	var panicErr *recoveredPanicError
	require.ErrorAs(t, err, &panicErr)
	require.Equal(t, "test panic panic: boom", err.Error())
}

func TestRegularWorkerDetermineStartingBlockUpdatesCatchupRegistry(t *testing.T) {
	t.Parallel()

	chain := &stubIndexer{
		name:         "ethereum",
		internalCode: "ETH",
		networkType:  enum.NetworkTypeEVM,
		latest:       25,
	}
	store := &stubBlockStore{latestBlock: 20}
	statusRegistry := status.NewRegistry()
	statusRegistry.RegisterChain("ethereum", "ethereum", config.ChainConfig{
		NetworkId:    "eth-mainnet",
		InternalCode: "ETH",
		Type:         enum.NetworkTypeEVM,
	})
	rw := newTestRegularWorker(chain, store, 20, 2)
	rw.statusRegistry = statusRegistry

	start := rw.determineStartingBlock()
	require.Equal(t, uint64(25), start)

	resp := statusRegistry.Snapshot("1.0.0")
	require.Len(t, resp.Networks, 1)
	require.Equal(t, 1, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(5), resp.Networks[0].CatchupPendingBlocks)
}

func TestRegularWorkerDetermineStartingBlockStoreDownChainUp(t *testing.T) {
	t.Parallel()

	// Store down but chain reachable: anchor on the chain head.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latest: 500}
	store := &stubBlockStore{getLatestBlockErr: errors.New("redis down")}
	rw := newTestRegularWorker(chain, store, 0, 2)

	require.Equal(t, uint64(500), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockWaitsForChainHead(t *testing.T) {
	t.Parallel()

	// Cold start with chain failing transiently: wait until the head responds.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latest: 500, latestFailN: 2}
	store := &stubBlockStore{latestBlock: 0}
	rw := newTestRegularWorker(chain, store, 0, 2)
	rw.startBlockRetryDelay = time.Millisecond

	require.Equal(t, uint64(500), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockColdStartChainUp(t *testing.T) {
	t.Parallel()

	// Cold start with chain reachable: start from chain head.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latest: 500}
	store := &stubBlockStore{latestBlock: 0}
	rw := newTestRegularWorker(chain, store, 0, 2)

	require.Equal(t, uint64(500), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockColdStartUsesConfiguredStartBlock(t *testing.T) {
	t.Parallel()

	// Cold start with a healthy store and a configured start_block: anchor on it
	// rather than the chain head, so a backfill begins from the chosen height.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latest: 500}
	store := &stubBlockStore{latestBlock: 0}
	rw := newTestRegularWorker(chain, store, 0, 2)
	rw.config.StartBlock = 100

	require.Equal(t, uint64(100), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockResumeIgnoresStartBlock(t *testing.T) {
	t.Parallel()

	// A prior KV checkpoint always wins over start_block: resume from the head
	// (queuing catchup for the gap), never rewind to the configured start_block.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latest: 500}
	store := &stubBlockStore{latestBlock: 300}
	rw := newTestRegularWorker(chain, store, 300, 2)
	rw.config.StartBlock = 100

	require.Equal(t, uint64(500), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockStoreDownIgnoresStartBlock(t *testing.T) {
	t.Parallel()

	// Store down: fall back to the chain head even when start_block is set, so a
	// broken store never re-triggers a backfill from start_block on each restart.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latest: 500}
	store := &stubBlockStore{getLatestBlockErr: errors.New("redis down")}
	rw := newTestRegularWorker(chain, store, 0, 2)
	rw.config.StartBlock = 100

	require.Equal(t, uint64(500), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockChainCancelledReturnsZero(t *testing.T) {
	t.Parallel()

	// Cold start, chain never responds and context is cancelled: return 0.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latestErr: errors.New("tls: internal error")}
	store := &stubBlockStore{latestBlock: 0}
	rw := newTestRegularWorker(chain, store, 0, 2)
	rw.startBlockRetryDelay = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rw.ctx = ctx

	require.Equal(t, uint64(0), rw.determineStartingBlock())
}

func TestRegularWorkerDetermineStartingBlockChainDownResumesFromKV(t *testing.T) {
	t.Parallel()

	// Chain RPC down but store has a known prior block: resume from KV latest.
	chain := &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM, latestErr: errors.New("tls: internal error")}
	store := &stubBlockStore{latestBlock: 42}
	rw := newTestRegularWorker(chain, store, 0, 2)
	rw.startBlockRetryDelay = time.Millisecond

	require.Equal(t, uint64(42), rw.determineStartingBlock())
}

func TestCatchupWorkerUpdatesCatchupRegistryOnProgressAndCompletion(t *testing.T) {
	t.Parallel()

	statusRegistry := status.NewRegistry()
	statusRegistry.RegisterChain("ethereum", "ethereum", config.ChainConfig{
		NetworkId:    "eth-mainnet",
		InternalCode: "ETH",
		Type:         enum.NetworkTypeEVM,
	})
	statusRegistry.SetCatchupRanges("ethereum", []blockstore.CatchupRange{{
		Start:   1,
		End:     10,
		Current: 0,
	}})

	store := &stubBlockStore{}
	cw := &CatchupWorker{
		BaseWorker: &BaseWorker{
			ctx:            context.Background(),
			cancel:         func() {},
			logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
			chain:          &stubIndexer{name: "ethereum", internalCode: "ETH", networkType: enum.NetworkTypeEVM},
			blockStore:     store,
			statusRegistry: statusRegistry,
		},
		blockRanges: []blockstore.CatchupRange{{
			Start:   1,
			End:     10,
			Current: 0,
		}},
	}

	cw.saveProgress(blockstore.CatchupRange{Start: 1, End: 10, Current: 0}, 5)

	resp := statusRegistry.Snapshot("1.0.0")
	require.Len(t, resp.Networks, 1)
	require.Equal(t, 1, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(5), resp.Networks[0].CatchupPendingBlocks)

	err := cw.completeRange(blockstore.CatchupRange{Start: 1, End: 10, Current: 5})
	require.NoError(t, err)

	resp = statusRegistry.Snapshot("1.0.0")
	require.Equal(t, 0, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(0), resp.Networks[0].CatchupPendingBlocks)
}

func testChainConfig() config.ChainConfig {
	return config.ChainConfig{
		PollInterval: time.Millisecond,
		Throttle: config.Throttle{
			BatchSize: 2,
		},
	}
}

func newTestRegularWorker(chain *stubIndexer, store *stubBlockStore, currentBlock uint64, batchSize int) *RegularWorker {
	cfg := testChainConfig()
	cfg.Throttle.BatchSize = batchSize

	return &RegularWorker{
		BaseWorker: &BaseWorker{
			ctx:        context.Background(),
			cancel:     func() {},
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			config:     cfg,
			chain:      chain,
			blockStore: store,
			failedChan: make(chan FailedBlockEvent, 1),
		},
		currentBlock: currentBlock,
		blockHashes:  make([]blockstore.BlockHashEntry, 0, MaxBlockHashSize),
	}
}

type stubIndexer struct {
	name          string
	internalCode  string
	networkType   enum.NetworkType
	latest        uint64
	latestErr     error
	latestFailN   int // fail GetLatestBlockNumber this many times before succeeding
	getBlocksFunc func(ctx context.Context, from, to uint64, isParallel bool) ([]indexer.BlockResult, error)
	getBlockFunc  func(ctx context.Context, number uint64) (*types.Block, error)
	getBlockCalls []uint64
}

func (s *stubIndexer) GetName() string {
	return s.name
}

func (s *stubIndexer) GetNetworkType() enum.NetworkType {
	return s.networkType
}

func (s *stubIndexer) GetNetworkInternalCode() string {
	return s.internalCode
}

func (s *stubIndexer) GetLatestBlockNumber(context.Context) (uint64, error) {
	if s.latestFailN > 0 {
		s.latestFailN--
		return 0, errors.New("tls: internal error")
	}
	if s.latestErr != nil {
		return 0, s.latestErr
	}
	return s.latest, nil
}

func (s *stubIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	s.getBlockCalls = append(s.getBlockCalls, number)
	if s.getBlockFunc != nil {
		return s.getBlockFunc(ctx, number)
	}
	return nil, errors.New("not implemented")
}

func (s *stubIndexer) GetBlocks(ctx context.Context, from, to uint64, isParallel bool) ([]indexer.BlockResult, error) {
	if s.getBlocksFunc != nil {
		return s.getBlocksFunc(ctx, from, to, isParallel)
	}
	return nil, errors.New("not implemented")
}

func (s *stubIndexer) GetBlocksByNumbers(context.Context, []uint64) ([]indexer.BlockResult, error) {
	return nil, errors.New("not implemented")
}

func (s *stubIndexer) IsHealthy() bool {
	return true
}

type stubBlockStore struct {
	mu                     sync.Mutex
	latestBlock            uint64
	savedLatest            []uint64
	failedBlocks           []uint64
	savedCatchupRanges     []blockstore.CatchupRange
	catchupProgress        []blockstore.CatchupRange
	deleteCatchupCalls     []blockstore.CatchupRange
	getLatestBlockErr      error
	getCatchupProgressErr  error
	saveCatchupRangesErr   error
	saveCatchupProgressErr error
	deleteCatchupErr       error
}

// hasDeleted reports whether DeleteCatchupRange was called for [start, end].
func (s *stubBlockStore) hasDeleted(start, end uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.deleteCatchupCalls {
		if r.Start == start && r.End == end {
			return true
		}
	}
	return false
}

func (s *stubBlockStore) GetLatestBlock(string) (uint64, error) {
	if s.getLatestBlockErr != nil {
		return 0, s.getLatestBlockErr
	}
	// Mirrors the real store contract: a missing key (cold start) returns
	// (0, nil); only genuine store errors return a non-nil error.
	return s.latestBlock, nil
}

func (s *stubBlockStore) SaveLatestBlock(_ string, blockNumber uint64) error {
	s.savedLatest = append(s.savedLatest, blockNumber)
	return nil
}

func (s *stubBlockStore) GetFailedBlocks(string) ([]uint64, error) {
	return s.failedBlocks, nil
}

func (s *stubBlockStore) SaveFailedBlock(_ string, blockNumber uint64) error {
	s.failedBlocks = append(s.failedBlocks, blockNumber)
	return nil
}

func (s *stubBlockStore) SaveFailedBlocks(string, []uint64) error {
	return nil
}

func (s *stubBlockStore) RemoveFailedBlocks(string, []uint64) error {
	return nil
}

func (s *stubBlockStore) SaveCatchupRanges(_ string, ranges []blockstore.CatchupRange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveCatchupRangesErr != nil {
		return s.saveCatchupRangesErr
	}
	s.savedCatchupRanges = append(s.savedCatchupRanges, ranges...)
	return nil
}

func (s *stubBlockStore) SaveCatchupProgress(_ string, start, end, current uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveCatchupProgressErr != nil {
		return s.saveCatchupProgressErr
	}
	for i := range s.catchupProgress {
		if s.catchupProgress[i].Start == start && s.catchupProgress[i].End == end {
			s.catchupProgress[i].Current = current
			return nil
		}
	}
	s.catchupProgress = append(s.catchupProgress, blockstore.CatchupRange{
		Start:   start,
		End:     end,
		Current: current,
	})
	return nil
}

func (s *stubBlockStore) GetCatchupProgress(string) ([]blockstore.CatchupRange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getCatchupProgressErr != nil {
		return nil, s.getCatchupProgressErr
	}
	return append([]blockstore.CatchupRange(nil), s.catchupProgress...), nil
}

func (s *stubBlockStore) DeleteCatchupRange(_ string, start, end uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteCatchupErr != nil {
		return s.deleteCatchupErr
	}
	s.deleteCatchupCalls = append(s.deleteCatchupCalls, blockstore.CatchupRange{
		Start: start,
		End:   end,
	})
	filtered := s.catchupProgress[:0]
	for _, rng := range s.catchupProgress {
		if rng.Start == start && rng.End == end {
			continue
		}
		filtered = append(filtered, rng)
	}
	s.catchupProgress = filtered
	return nil
}

func (s *stubBlockStore) GetBlockHashes(string) ([]blockstore.BlockHashEntry, error) {
	return nil, nil
}

func (s *stubBlockStore) SaveBlockHashes(string, []blockstore.BlockHashEntry) error {
	return nil
}

func (s *stubBlockStore) Close() error {
	return nil
}
