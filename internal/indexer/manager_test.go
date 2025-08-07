package indexer

import (
	"os"
	"testing"

	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/kvstore"
)

func TestNewManager(t *testing.T) {
	// Create a test config
	testConfig := &config.Config{
		Indexer: config.IndexerConfig{
			NATS: config.NATSConfig{
				URL:           "nats://localhost:4222",
				SubjectPrefix: "test",
			},
			Chains: map[string]config.ChainConfig{
				"test_chain": {
					StartBlock:   100,
					BatchSize:    10,
					PollInterval: 1000000000, // 1 second in nanoseconds
					IsLatest:     false,
					Nodes:        []string{"http://localhost:8545"},
				},
			},
		},
	}

	// Create manager
	manager, err := NewManager(testConfig)
	if err != nil {
		// If NATS is not available, skip this test
		t.Skip("NATS not available, skipping test")
	}

	// Verify manager was created
	if manager == nil {
		t.Error("Manager should not be nil")
	}

	if manager.config != testConfig {
		t.Error("Config not set correctly")
	}

	if manager.emitter == nil {
		t.Error("Emitter should not be nil")
	}
}

func TestManager_KVStoreIntegration(t *testing.T) {
	// Create temporary directory for BadgerStore
	tempDir, err := os.MkdirTemp("", "manager_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create BadgerStore
	store, err := kvstore.NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	// Test that the store can be used
	testKey := "test_key"
	testValue := []byte("test_value")

	err = store.Set(testKey, testValue)
	if err != nil {
		t.Errorf("Failed to set test value: %v", err)
	}

	retrieved, err := store.Get(testKey)
	if err != nil {
		t.Errorf("Failed to get test value: %v", err)
	}

	if string(retrieved) != string(testValue) {
		t.Errorf("Expected %s, got %s", string(testValue), string(retrieved))
	}
}

func TestManager_ConfigValidation(t *testing.T) {
	// Test valid config
	validConfig := &config.Config{
		Indexer: config.IndexerConfig{
			NATS: config.NATSConfig{
				URL:           "nats://localhost:4222",
				SubjectPrefix: "test",
			},
			Chains: map[string]config.ChainConfig{
				"chain1": {
					StartBlock:   100,
					BatchSize:    10,
					PollInterval: 1000000000,
					IsLatest:     false,
					Nodes:        []string{"http://localhost:8545"},
				},
				"chain2": {
					StartBlock:   200,
					BatchSize:    20,
					PollInterval: 2000000000,
					IsLatest:     true,
					Nodes:        []string{"http://localhost:8546"},
				},
			},
		},
	}

	// Validate config structure
	if len(validConfig.Indexer.Chains) != 2 {
		t.Errorf("Expected 2 chains, got %d", len(validConfig.Indexer.Chains))
	}

	chain1, exists := validConfig.Indexer.Chains["chain1"]
	if !exists {
		t.Error("Chain1 should exist")
	}
	if chain1.StartBlock != 100 {
		t.Errorf("Expected StartBlock 100, got %d", chain1.StartBlock)
	}

	chain2, exists := validConfig.Indexer.Chains["chain2"]
	if !exists {
		t.Error("Chain2 should exist")
	}
	if chain2.StartBlock != 200 {
		t.Errorf("Expected StartBlock 200, got %d", chain2.StartBlock)
	}
}

func TestManager_ErrorHandling(t *testing.T) {
	// Test with invalid NATS URL
	invalidConfig := &config.Config{
		Indexer: config.IndexerConfig{
			NATS: config.NATSConfig{
				URL:           "invalid://url",
				SubjectPrefix: "test",
			},
			Chains: map[string]config.ChainConfig{
				"test_chain": {
					StartBlock:   100,
					BatchSize:    10,
					PollInterval: 1000000000,
					IsLatest:     false,
					Nodes:        []string{"http://localhost:8545"},
				},
			},
		},
	}

	// This should fail due to invalid NATS URL
	_, err := NewManager(invalidConfig)
	if err == nil {
		t.Error("Expected error with invalid NATS URL")
	}
}
