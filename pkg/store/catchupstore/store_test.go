package catchupstore

import (
	"context"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCatchupStoreRoundTripAndDelete(t *testing.T) {
	t.Parallel()

	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := New(&realRedisClient{client: client})
	ctx := context.Background()

	err := store.SaveRanges(ctx, "eth", []blockstore.CatchupRange{
		{Start: 21, End: 30, Current: 20},
		{Start: 1, End: 10, Current: 5},
	})
	require.NoError(t, err)

	err = store.SaveProgress(ctx, "eth", 21, 30, 25)
	require.NoError(t, err)

	ranges, err := store.GetProgress(ctx, "eth")
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{
		{Start: 1, End: 10, Current: 5},
		{Start: 21, End: 30, Current: 25},
	}, ranges)

	err = store.DeleteRange(ctx, "eth", 1, 10)
	require.NoError(t, err)

	ranges, err = store.GetProgress(ctx, "eth")
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{
		{Start: 21, End: 30, Current: 25},
	}, ranges)

	err = store.DeleteRange(ctx, "eth", 21, 30)
	require.NoError(t, err)

	ranges, err = store.GetProgress(ctx, "eth")
	require.NoError(t, err)
	require.Empty(t, ranges)

	exists, err := client.Exists(ctx, composeKey("eth")).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
}

func TestCatchupStoreSkipsMalformedEntries(t *testing.T) {
	t.Parallel()

	client, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, client.HSet(ctx, composeKey("eth"),
		"bad", "1",
		"5-4", "2",
		"10-20", "NaN",
		"1-9", "3",
	).Err())

	store := New(&realRedisClient{client: client})
	ranges, err := store.GetProgress(ctx, "eth")
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{
		{Start: 1, End: 9, Current: 3},
	}, ranges)
}

func TestCatchupStoreSaveRangesMergesAdjacentAndOverlappingRanges(t *testing.T) {
	t.Parallel()

	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := New(&realRedisClient{client: client})
	ctx := context.Background()

	err := store.SaveRanges(ctx, "apt", []blockstore.CatchupRange{
		{Start: 1, End: 10, Current: 5},
		{Start: 11, End: 20, Current: 10},
		{Start: 8, End: 15, Current: 7},
		{Start: 30, End: 40, Current: 29},
	})
	require.NoError(t, err)

	ranges, err := store.GetProgress(ctx, "apt")
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{
		{Start: 1, End: 20, Current: 5},
		{Start: 30, End: 40, Current: 29},
	}, ranges)
}

func TestCatchupStoreSaveRangesMergesWithExistingRedisRanges(t *testing.T) {
	t.Parallel()

	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := New(&realRedisClient{client: client})
	ctx := context.Background()

	err := store.SaveRanges(ctx, "apt", []blockstore.CatchupRange{
		{Start: 100, End: 120, Current: 110},
	})
	require.NoError(t, err)

	err = store.SaveRanges(ctx, "apt", []blockstore.CatchupRange{
		{Start: 121, End: 140, Current: 120},
		{Start: 130, End: 150, Current: 129},
	})
	require.NoError(t, err)

	ranges, err := store.GetProgress(ctx, "apt")
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{
		{Start: 100, End: 150, Current: 110},
	}, ranges)
}

type realRedisClient struct {
	client *redis.Client
}

func (r *realRedisClient) GetClient() *redis.Client {
	return r.client
}

func (r *realRedisClient) Set(key string, value any, expiration time.Duration) error {
	return r.client.Set(context.Background(), key, value, expiration).Err()
}

func (r *realRedisClient) Get(key string) (string, error) {
	return r.client.Get(context.Background(), key).Result()
}

func (r *realRedisClient) Del(keys ...string) error {
	return r.client.Del(context.Background(), keys...).Err()
}

func (r *realRedisClient) ZAdd(key string, members ...redis.Z) error {
	return r.client.ZAdd(context.Background(), key, members...).Err()
}

func (r *realRedisClient) ZRem(key string, members ...interface{}) error {
	return r.client.ZRem(context.Background(), key, members...).Err()
}

func (r *realRedisClient) ZRange(key string, start, stop int64) ([]string, error) {
	return r.client.ZRange(context.Background(), key, start, stop).Result()
}

func (r *realRedisClient) ZRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return r.client.ZRangeWithScores(context.Background(), key, start, stop).Result()
}

func (r *realRedisClient) ZRevRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return r.client.ZRevRangeWithScores(context.Background(), key, start, stop).Result()
}

func (r *realRedisClient) Close() error {
	return r.client.Close()
}

func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()

	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   14,
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
