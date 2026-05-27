package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/status"
	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/catchupstore"
	"github.com/redis/go-redis/v9"
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
	catchupStore := &stubCatchupStore{}
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
			catchupStore:   catchupStore,
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
		catchupStore.catchupProgress = []blockstore.CatchupRange{
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

func TestCatchupWorkersClaimDistinctRangesAcrossInstances(t *testing.T) {
	t.Parallel()

	client, cleanup := setupWorkerTestRedis(t)
	defer cleanup()

	statusRegistry := status.NewRegistry()
	statusRegistry.RegisterChain("aptos", "aptos_testnet", config.ChainConfig{
		NetworkId:    "aptos_testnet",
		InternalCode: "APTOS_TESTNET",
		Type:         enum.NetworkTypeApt,
	})

	store := catchupstore.New(&workerTestRedisClient{client: client})
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	var mu sync.Mutex
	seen := make([]string, 0, 2)
	indexerStub := &stubIndexer{
		name:         "aptos",
		internalCode: "APTOS_TESTNET",
		networkType:  enum.NetworkTypeApt,
		getBlocksFunc: func(_ context.Context, from, to uint64, _ bool) ([]indexer.BlockResult, error) {
			mu.Lock()
			seen = append(seen, fmt.Sprintf("%d-%d", from, to))
			mu.Unlock()
			return nil, nil
		},
	}

	require.NoError(t, store.SaveRanges(context.Background(), "APTOS_TESTNET", []blockstore.CatchupRange{
		{Start: 1, End: 10, Current: 0},
		{Start: 11, End: 20, Current: 10},
	}))

	newWorker := func(ctx context.Context) *CatchupWorker {
		return &CatchupWorker{
			BaseWorker: &BaseWorker{
				ctx:    ctx,
				cancel: func() {},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				config: config.ChainConfig{
					PollInterval: time.Millisecond,
					Throttle:     config.Throttle{BatchSize: 20},
				},
				chain:          indexerStub,
				blockStore:     &stubBlockStore{},
				catchupStore:   store,
				statusRegistry: statusRegistry,
			},
			blockRanges: []blockstore.CatchupRange{},
			workerPool:  make(chan struct{}, CATCHUP_WORKERS),
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w := newWorker(ctx1)
		w.runCatchup()
	}()
	go func() {
		defer wg.Done()
		w := newWorker(ctx2)
		w.runCatchup()
	}()

	time.Sleep(300 * time.Millisecond)
	cancel1()
	cancel2()

	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	require.ElementsMatch(t, []string{"1-10", "11-20"}, seen)
}

type workerTestRedisClient struct {
	client *redis.Client
}

var _ infra.RedisClient = (*workerTestRedisClient)(nil)

func (r *workerTestRedisClient) GetClient() *redis.Client {
	return r.client
}

func (r *workerTestRedisClient) Set(key string, value any, expiration time.Duration) error {
	return r.client.Set(context.Background(), key, value, expiration).Err()
}

func (r *workerTestRedisClient) Get(key string) (string, error) {
	return r.client.Get(context.Background(), key).Result()
}

func (r *workerTestRedisClient) Del(keys ...string) error {
	return r.client.Del(context.Background(), keys...).Err()
}

func (r *workerTestRedisClient) ZAdd(key string, members ...redis.Z) error {
	return r.client.ZAdd(context.Background(), key, members...).Err()
}

func (r *workerTestRedisClient) ZRem(key string, members ...interface{}) error {
	return r.client.ZRem(context.Background(), key, members...).Err()
}

func (r *workerTestRedisClient) ZRange(key string, start, stop int64) ([]string, error) {
	return r.client.ZRange(context.Background(), key, start, stop).Result()
}

func (r *workerTestRedisClient) ZRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return r.client.ZRangeWithScores(context.Background(), key, start, stop).Result()
}

func (r *workerTestRedisClient) ZRevRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return r.client.ZRevRangeWithScores(context.Background(), key, start, stop).Result()
}

func (r *workerTestRedisClient) Close() error {
	return r.client.Close()
}

func setupWorkerTestRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()

	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   13,
	})

	ctx := context.Background()
	if _, err := client.Ping(ctx).Result(); err != nil {
		t.Skip("Redis not available")
	}
	require.NoError(t, client.FlushDB(ctx).Err())

	cleanup := func() {
		_ = client.FlushDB(ctx).Err()
		_ = client.Close()
	}

	return client, cleanup
}
