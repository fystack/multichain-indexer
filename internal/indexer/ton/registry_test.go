package ton

import (
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

type mockRedisClient struct {
	data map[string]string
}

func (m *mockRedisClient) GetClient() *redis.Client { return nil }
func (m *mockRedisClient) Set(key string, value any, _ time.Duration) error {
	if m.data == nil {
		m.data = make(map[string]string)
	}
	switch v := value.(type) {
	case string:
		m.data[key] = v
	default:
		m.data[key] = ""
	}
	return nil
}
func (m *mockRedisClient) Get(key string) (string, error) { return m.data[key], nil }
func (m *mockRedisClient) Del(_ ...string) error          { return nil }
func (m *mockRedisClient) ZAdd(_ string, _ ...redis.Z) error {
	return nil
}
func (m *mockRedisClient) ZRem(_ string, _ ...interface{}) error {
	return nil
}
func (m *mockRedisClient) ZRange(_ string, _, _ int64) ([]string, error) {
	return nil, nil
}
func (m *mockRedisClient) ZRangeWithScores(_ string, _, _ int64) ([]redis.Z, error) {
	return nil, nil
}
func (m *mockRedisClient) ZRevRangeWithScores(_ string, _, _ int64) ([]redis.Z, error) {
	return nil, nil
}
func (m *mockRedisClient) Close() error { return nil }

var _ infra.RedisClient = (*mockRedisClient)(nil)

func TestConfigBasedRegistryGetInfoByWalletFallback(t *testing.T) {
	reg := NewConfigBasedRegistry(nil)
	reg.RegisterWallet("wallet-1", "master-1")

	info, ok := reg.GetInfoByWallet("wallet-1")
	assert.True(t, ok)
	assert.Equal(t, "master-1", info.MasterAddress)
}

func TestRedisJettonRegistryRegisterWalletPersistsAndFallback(t *testing.T) {
	redisClient := &mockRedisClient{data: make(map[string]string)}
	reg := NewRedisJettonRegistry("ton_testnet", redisClient, nil)

	reg.RegisterWallet("wallet-1", "master-1")

	info, ok := reg.GetInfoByWallet("wallet-1")
	assert.True(t, ok)
	assert.Equal(t, "master-1", info.MasterAddress)

	raw, exists := redisClient.data[reg.walletMappingKey()]
	assert.True(t, exists)
	assert.Contains(t, raw, "\"wallet-1\":\"master-1\"")
}
