package catchupstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/stretchr/testify/require"
)

func dialRedis(t *testing.T) infra.RedisClient {
	t.Helper()
	rc, err := infra.NewRedisClient("localhost:6379", "", "test", false)
	if err != nil {
		t.Skip("Redis not available, skipping test")
	}
	if err := rc.GetClient().Ping(context.Background()).Err(); err != nil {
		t.Skip("Redis not available, skipping test")
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

func cleanupChain(t *testing.T, rc infra.RedisClient, chain string) {
	t.Helper()
	ctx := context.Background()
	keys := []string{composeKey(chain), composeMigratedKey(chain)}
	require.NoError(t, rc.GetClient().Del(ctx, keys...).Err())
	t.Cleanup(func() { _ = rc.GetClient().Del(ctx, keys...).Err() })
}

func TestSaveRangesAndGetProgressSorted(t *testing.T) {
	rc := dialRedis(t)
	chain := fmt.Sprintf("test_sorted_%d", 1)
	cleanupChain(t, rc, chain)

	s := New(rc, nil)
	ctx := context.Background()

	require.NoError(t, s.SaveRanges(ctx, chain, []blockstore.CatchupRange{
		{Start: 30, End: 39, Current: 29},
		{Start: 1, End: 10, Current: 5},
	}))

	got, err := s.GetProgress(ctx, chain)
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{
		{Start: 1, End: 10, Current: 5},
		{Start: 30, End: 39, Current: 29},
	}, got)
}

func TestSaveProgressUpdatesCurrent(t *testing.T) {
	rc := dialRedis(t)
	chain := "test_progress"
	cleanupChain(t, rc, chain)

	s := New(rc, nil)
	ctx := context.Background()

	require.NoError(t, s.SaveRanges(ctx, chain, []blockstore.CatchupRange{{Start: 1, End: 20, Current: 0}}))
	require.NoError(t, s.SaveProgress(ctx, chain, 1, 20, 12))

	got, err := s.GetProgress(ctx, chain)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(12), got[0].Current)
}

func TestDeleteFinalRangeClearsKey(t *testing.T) {
	rc := dialRedis(t)
	chain := "test_delete"
	cleanupChain(t, rc, chain)

	s := New(rc, nil)
	ctx := context.Background()

	require.NoError(t, s.SaveRanges(ctx, chain, []blockstore.CatchupRange{{Start: 1, End: 5, Current: 0}}))
	require.NoError(t, s.DeleteRange(ctx, chain, 1, 5))

	exists, err := rc.GetClient().Exists(ctx, composeKey(chain)).Result()
	require.NoError(t, err)
	require.Equal(t, int64(0), exists, "hash key should be removed once empty")
}

func TestSaveRangesMergesOverlapping(t *testing.T) {
	rc := dialRedis(t)
	chain := "test_merge"
	cleanupChain(t, rc, chain)

	s := New(rc, nil)
	ctx := context.Background()

	require.NoError(t, s.SaveRanges(ctx, chain, []blockstore.CatchupRange{{Start: 1, End: 10, Current: 4}}))
	// Adjacent range should merge into a single field.
	require.NoError(t, s.SaveRanges(ctx, chain, []blockstore.CatchupRange{{Start: 11, End: 20, Current: 15}}))

	got, err := s.GetProgress(ctx, chain)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, blockstore.CatchupRange{Start: 1, End: 20, Current: 4}, got[0])
}

// fakeKV is a minimal infra.KVStore exposing seeded catchup pairs via List.
type fakeKV struct {
	pairs []*infra.KVPair
}

func (fakeKV) GetName() string                  { return "fake" }
func (fakeKV) Set(string, string) error         { return nil }
func (fakeKV) Get(string) (string, error)       { return "", nil }
func (fakeKV) SetAny(string, any) error         { return nil }
func (fakeKV) GetAny(string, any) (bool, error) { return false, nil }
func (f fakeKV) List(prefix string) ([]*infra.KVPair, error) {
	var out []*infra.KVPair
	for _, p := range f.pairs {
		if len(p.Key) >= len(prefix) && p.Key[:len(prefix)] == prefix {
			out = append(out, p)
		}
	}
	return out, nil
}
func (fakeKV) Delete(string) error           { return nil }
func (fakeKV) BatchSet([]infra.KVPair) error { return nil }
func (fakeKV) Close() error                  { return nil }

func TestLazyMigrationFromLegacyKV(t *testing.T) {
	rc := dialRedis(t)
	chain := "test_migrate"
	cleanupChain(t, rc, chain)

	// Legacy one-key-per-range entry: block_states/<chain>/catchup_progress/1-20 -> 10
	legacyKey := fmt.Sprintf(
		"%s/%s/%s/%d-%d",
		blockstore.BlockStates, chain, constant.KVPrefixProgressCatchup, 1, 20,
	)
	legacy := blockstore.NewBlockStore(fakeKV{pairs: []*infra.KVPair{{
		Key:   legacyKey,
		Value: []byte("10"),
	}}})

	s := New(rc, legacy)
	ctx := context.Background()

	got, err := s.GetProgress(ctx, chain)
	require.NoError(t, err)
	require.Equal(t, []blockstore.CatchupRange{{Start: 1, End: 20, Current: 10}}, got)

	// Marker must suppress a second migration even after the hash empties.
	require.NoError(t, s.DeleteRange(ctx, chain, 1, 20))
	got, err = s.GetProgress(ctx, chain)
	require.NoError(t, err)
	require.Empty(t, got, "completed ranges must not be re-migrated from legacy KV")
}
