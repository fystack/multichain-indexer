package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
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

func TestCheckContinuityReturnsFalseForNilBlocks(t *testing.T) {
	t.Parallel()

	require.False(t, checkContinuity(indexer.BlockResult{}, indexer.BlockResult{}))
	require.False(t, checkContinuity(
		indexer.BlockResult{Block: &types.Block{Hash: "0x100"}},
		indexer.BlockResult{},
	))
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
		blockHashes:  make([]BlockHashEntry, 0, MaxBlockHashSize),
	}
}

type stubIndexer struct {
	name          string
	internalCode  string
	networkType   enum.NetworkType
	latest        uint64
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
	savedLatest  []uint64
	failedBlocks []uint64
}

func (s *stubBlockStore) GetLatestBlock(string) (uint64, error) {
	return 0, errors.New("not found")
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

func (s *stubBlockStore) SaveCatchupRanges(string, []blockstore.CatchupRange) error {
	return nil
}

func (s *stubBlockStore) SaveCatchupProgress(string, uint64, uint64, uint64) error {
	return nil
}

func (s *stubBlockStore) GetCatchupProgress(string) ([]blockstore.CatchupRange, error) {
	return nil, nil
}

func (s *stubBlockStore) DeleteCatchupRange(string, uint64, uint64) error {
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
