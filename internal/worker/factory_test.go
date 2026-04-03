package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	commonlogger "github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/hashicorp/consul/api"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCreateManagerWithWorkersUsesPerChainFailedChannels(t *testing.T) {
	t.Parallel()
	initTestLogger()

	cfg := &config.Config{
		Chains: config.Chains{
			"chain-a": {
				Name:         "chain-a",
				NetworkId:    "chain-a",
				InternalCode: "a",
				Type:         enum.NetworkTypeEVM,
				PollInterval: time.Millisecond,
				Client: config.ClientConfig{
					Timeout: time.Second,
				},
				Throttle: config.Throttle{
					BatchSize: 1,
					RPS:       1,
					Burst:     1,
				},
				Nodes: []config.NodeConfig{{URL: "http://127.0.0.1:8545"}},
			},
			"chain-b": {
				Name:         "chain-b",
				NetworkId:    "chain-b",
				InternalCode: "b",
				Type:         enum.NetworkTypeEVM,
				PollInterval: time.Millisecond,
				Client: config.ClientConfig{
					Timeout: time.Second,
				},
				Throttle: config.Throttle{
					BatchSize: 1,
					RPS:       1,
					Burst:     1,
				},
				Nodes: []config.NodeConfig{{URL: "http://127.0.0.1:8546"}},
			},
		},
		Services: config.Services{
			Worker: config.WorkerConfig{
				Manual:    config.WorkerModeConfig{Enabled: true},
				Rescanner: config.WorkerModeConfig{Enabled: true},
			},
		},
	}

	manager := CreateManagerWithWorkers(
		context.Background(),
		cfg,
		noopKVStore{},
		nil,
		nil,
		events.Emitter(nil),
		nil,
		ManagerConfig{
			Chains: []string{"chain-a", "chain-b"},
		},
	)

	channelsByChain := make(map[string]map[WorkerMode]chan FailedBlockEvent)
	for _, worker := range manager.workers {
		switch w := worker.(type) {
		case *ManualWorker:
			if channelsByChain[w.chain.GetName()] == nil {
				channelsByChain[w.chain.GetName()] = make(map[WorkerMode]chan FailedBlockEvent)
			}
			channelsByChain[w.chain.GetName()][ModeManual] = w.failedChan
		case *RescannerWorker:
			if channelsByChain[w.chain.GetName()] == nil {
				channelsByChain[w.chain.GetName()] = make(map[WorkerMode]chan FailedBlockEvent)
			}
			channelsByChain[w.chain.GetName()][ModeRescanner] = w.failedChan
		}
	}

	require.Len(t, channelsByChain, 2)
	require.NotNil(t, channelsByChain["CHAIN-A"][ModeManual])
	require.NotNil(t, channelsByChain["CHAIN-A"][ModeRescanner])
	require.NotNil(t, channelsByChain["CHAIN-B"][ModeManual])
	require.NotNil(t, channelsByChain["CHAIN-B"][ModeRescanner])

	require.True(t, channelsByChain["CHAIN-A"][ModeManual] == channelsByChain["CHAIN-A"][ModeRescanner])
	require.True(t, channelsByChain["CHAIN-B"][ModeManual] == channelsByChain["CHAIN-B"][ModeRescanner])
	require.True(t, channelsByChain["CHAIN-A"][ModeManual] != channelsByChain["CHAIN-B"][ModeManual])
}

func TestRescannerFailedChannelIsolationByChain(t *testing.T) {
	t.Parallel()
	initTestLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chainA := &stubIndexer{name: "chain-a", internalCode: "a", networkType: enum.NetworkTypeEVM}
	chainB := &stubIndexer{name: "chain-b", internalCode: "b", networkType: enum.NetworkTypeEVM}

	chA := make(chan FailedBlockEvent, 1)
	chB := make(chan FailedBlockEvent, 1)

	rwA := NewRescannerWorker(ctx, chainA, testChainConfig(), noopKVStore{}, &stubBlockStore{}, events.Emitter(nil), nil, chA, nil)
	rwB := NewRescannerWorker(ctx, chainB, testChainConfig(), noopKVStore{}, &stubBlockStore{}, events.Emitter(nil), nil, chB, nil)

	doneA := make(chan struct{})
	doneB := make(chan struct{})

	go func() {
		evt := <-rwA.failedChan
		rwA.addFailedBlock(evt.Block, "test chain A")
		close(doneA)
	}()
	go func() {
		select {
		case evt := <-rwB.failedChan:
			rwB.addFailedBlock(evt.Block, "unexpected cross-chain event")
		case <-time.After(50 * time.Millisecond):
		}
		close(doneB)
	}()

	chA <- FailedBlockEvent{Chain: "chain-a", Block: 101, Attempt: 1}

	<-doneA
	<-doneB

	require.Contains(t, rwA.failedBlocks, uint64(101))
	require.NotContains(t, rwB.failedBlocks, uint64(101))
}

func TestCreateManagerWithWorkersBootstrapsCatchupRangesIntoStatusRegistry(t *testing.T) {
	t.Parallel()
	initTestLogger()

	cfg := &config.Config{
		Chains: config.Chains{
			"chain-a": {
				Name:         "chain-a",
				NetworkId:    "chain-a",
				InternalCode: "a",
				Type:         enum.NetworkTypeEVM,
				PollInterval: time.Millisecond,
				Client: config.ClientConfig{
					Timeout: time.Second,
				},
				Throttle: config.Throttle{
					BatchSize: 1,
					RPS:       1,
					Burst:     1,
				},
				Nodes: []config.NodeConfig{{URL: "http://127.0.0.1:8545"}},
			},
		},
	}

	redisClient, cleanup := setupFactoryTestRedis(t)
	defer cleanup()
	require.NoError(t, redisClient.HSet(context.Background(), "catchup_progress:a", "1-20", "10").Err())

	manager := CreateManagerWithWorkers(
		context.Background(),
		cfg,
		noopKVStore{},
		nil,
		nil,
		events.Emitter(nil),
		&factoryTestRedisClient{client: redisClient},
		ManagerConfig{
			Chains: []string{"chain-a"},
		},
	)

	resp := manager.StatusSnapshot("1.0.0")
	require.Len(t, resp.Networks, 1)
	require.Equal(t, 1, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(10), resp.Networks[0].CatchupPendingBlocks)

	require.NoError(t, redisClient.Del(context.Background(), "catchup_progress:a").Err())
	require.NoError(t, redisClient.HSet(context.Background(), "catchup_progress:a", "31-40", "35").Err())

	resp = manager.StatusSnapshot("1.0.0")
	require.Len(t, resp.Networks, 1)
	require.Equal(t, 1, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(5), resp.Networks[0].CatchupPendingBlocks)
}

type noopKVStore struct{}

type listKVStore struct {
	pairs []*infra.KVPair
}

func initTestLogger() {
	commonlogger.Init(&commonlogger.Options{
		Level: slog.LevelError,
	})
}

func (noopKVStore) GetName() string            { return "noop" }
func (noopKVStore) Set(string, string) error   { return nil }
func (noopKVStore) Get(string) (string, error) { return "", errors.New("not found") }
func (noopKVStore) GetWithOptions(string, *api.QueryOptions) (string, error) {
	return "", errors.New("not found")
}
func (noopKVStore) SetAny(string, any) error             { return nil }
func (noopKVStore) GetAny(string, any) (bool, error)     { return false, nil }
func (noopKVStore) List(string) ([]*infra.KVPair, error) { return nil, nil }
func (noopKVStore) Delete(string) error                  { return nil }
func (noopKVStore) BatchSet([]infra.KVPair) error        { return nil }
func (noopKVStore) Close() error                         { return nil }

func (s *listKVStore) GetName() string            { return "list" }
func (s *listKVStore) Set(string, string) error   { return nil }
func (s *listKVStore) Get(string) (string, error) { return "", errors.New("not found") }
func (s *listKVStore) GetWithOptions(string, *api.QueryOptions) (string, error) {
	return "", errors.New("not found")
}
func (s *listKVStore) SetAny(string, any) error         { return nil }
func (s *listKVStore) GetAny(string, any) (bool, error) { return false, nil }
func (s *listKVStore) Delete(string) error              { return nil }
func (s *listKVStore) BatchSet([]infra.KVPair) error    { return nil }
func (s *listKVStore) Close() error                     { return nil }
func (s *listKVStore) List(prefix string) ([]*infra.KVPair, error) {
	var out []*infra.KVPair
	for _, pair := range s.pairs {
		if len(pair.Key) >= len(prefix) && pair.Key[:len(prefix)] == prefix {
			out = append(out, pair)
		}
	}
	return out, nil
}

type factoryTestRedisClient struct {
	client *redis.Client
}

func (r *factoryTestRedisClient) GetClient() *redis.Client { return r.client }
func (r *factoryTestRedisClient) Set(key string, value any, expiration time.Duration) error {
	return r.client.Set(context.Background(), key, value, expiration).Err()
}
func (r *factoryTestRedisClient) Get(key string) (string, error) {
	return r.client.Get(context.Background(), key).Result()
}
func (r *factoryTestRedisClient) Del(keys ...string) error {
	return r.client.Del(context.Background(), keys...).Err()
}
func (r *factoryTestRedisClient) ZAdd(key string, members ...redis.Z) error {
	return r.client.ZAdd(context.Background(), key, members...).Err()
}
func (r *factoryTestRedisClient) ZRem(key string, members ...interface{}) error {
	return r.client.ZRem(context.Background(), key, members...).Err()
}
func (r *factoryTestRedisClient) ZRange(key string, start, stop int64) ([]string, error) {
	return r.client.ZRange(context.Background(), key, start, stop).Result()
}
func (r *factoryTestRedisClient) ZRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return r.client.ZRangeWithScores(context.Background(), key, start, stop).Result()
}
func (r *factoryTestRedisClient) ZRevRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return r.client.ZRevRangeWithScores(context.Background(), key, start, stop).Result()
}
func (r *factoryTestRedisClient) Close() error { return r.client.Close() }

func setupFactoryTestRedis(t *testing.T) (*redis.Client, func()) {
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
