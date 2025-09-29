package addressbloomfilter

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"github.com/stretchr/testify/assert"
)

func TestRedisBloomFilter_AddAndContains(t *testing.T) {
	// Skip if Redis is not available
	redisAddr := "localhost:6379"
	if testing.Short() {
		t.Skip("Skipping Redis integration test in short mode")
	}

	client, err := infra.NewRedisClient(redisAddr, "", "test", false)
	if err != nil {
		t.Skipf("Cannot create Redis client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	// Check if RedisBloom module is available
	modules, err := client.GetClient().Do(ctx, "MODULE", "LIST").Result()
	if err != nil {
		t.Skipf("Cannot check Redis modules: %v", err)
	}

	hasBloom := false
	if modulesList, ok := modules.([]interface{}); ok {
		for _, module := range modulesList {
			if moduleMap, ok := module.(map[interface{}]interface{}); ok {
				if name, ok := moduleMap["name"].(string); ok && name == "bf" {
					hasBloom = true
					break
				}
			}
		}
	}

	if !hasBloom {
		t.Skip("RedisBloom module not available")
	}

	// Create a mock repository for testing
	mockRepo := &MockWalletAddressRepo{}

	// Create the bloom filter
	rbf := NewRedisBloomFilter(RedisBloomConfig{
		RedisClient:       client,
		WalletAddressRepo: mockRepo,
		KeyPrefix:         "test_bloom",
		ErrorRate:         0.01,
		Capacity:          1000,
		BatchSize:         100,
	})

	// Test addresses
	testAddresses := []string{
		"0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
		"0x1234567890123456789012345678901234567890",
		"TAWdqnuYCNU3dKsi7pR8d7sDkx1Evb2giV",
		"TT1j2adMBb6bF2K8C2LX1QkkmSXHjiaAfw",
	}

	addressType := enum.NetworkTypeEVM

	// Clear any existing bloom filter
	rbf.Clear(addressType)

	// Test 1: Add single address and check
	t.Run("AddSingleAddress", func(t *testing.T) {
		address := testAddresses[0]

		// Add the address
		rbf.Add(address, addressType)

		// Check if it exists
		exists := rbf.Contains(address, addressType)
		assert.True(t, exists, "Address should exist in bloom filter")

		// Check non-existent address
		nonExistent := "0x9999999999999999999999999999999999999999"
		notExists := rbf.Contains(nonExistent, addressType)
		assert.False(t, notExists, "Non-existent address should not be in bloom filter")
	})

	// Test 2: Add batch addresses and check
	t.Run("AddBatchAddresses", func(t *testing.T) {
		batchAddresses := testAddresses[1:3]

		// Add batch
		rbf.AddBatch(batchAddresses, addressType)

		// Check each address
		for _, addr := range batchAddresses {
			exists := rbf.Contains(addr, addressType)
			assert.True(t, exists, "Address %s should exist in bloom filter", addr)
		}

		// Check non-existent address
		nonExistent := "0x8888888888888888888888888888888888888888"
		notExists := rbf.Contains(nonExistent, addressType)
		assert.False(t, notExists, "Non-existent address should not be in bloom filter")
	})

	// Test 3: Test different address types
	t.Run("DifferentAddressTypes", func(t *testing.T) {
		evmAddress := testAddresses[0]
		tronAddress := testAddresses[2]

		// Add to different types
		rbf.Add(evmAddress, enum.NetworkTypeEVM)
		rbf.Add(tronAddress, enum.NetworkTypeTron)

		// Check EVM address in EVM filter
		exists := rbf.Contains(evmAddress, enum.NetworkTypeEVM)
		assert.True(t, exists, "EVM address should exist in EVM bloom filter")

		// Check Tron address in Tron filter
		exists = rbf.Contains(tronAddress, enum.NetworkTypeTron)
		assert.True(t, exists, "Tron address should exist in Tron bloom filter")

		// Check EVM address in Tron filter (should not exist)
		notExists := rbf.Contains(evmAddress, enum.NetworkTypeTron)
		assert.False(t, notExists, "EVM address should not exist in Tron bloom filter")
	})

	// Test 4: Clear and verify
	t.Run("ClearAndVerify", func(t *testing.T) {
		address := testAddresses[0]

		// Verify address exists
		exists := rbf.Contains(address, addressType)
		assert.True(t, exists, "Address should exist before clear")

		// Clear the filter
		rbf.Clear(addressType)

		// Verify address no longer exists
		notExists := rbf.Contains(address, addressType)
		assert.False(t, notExists, "Address should not exist after clear")
	})

	// Clean up
	rbf.Clear(addressType)
	rbf.Clear(enum.NetworkTypeTron)
}

// MockWalletAddressRepo for testing
type MockWalletAddressRepo struct{}

func (m *MockWalletAddressRepo) Find(
	ctx context.Context,
	options repository.FindOptions,
) ([]*model.WalletAddress, error) {
	// Return empty result for testing
	return []*model.WalletAddress{}, nil
}
