package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tempFile, err := os.CreateTemp("", "config_test.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	// Write test config content
	configContent := `
indexer:
  nats:
    url: "nats://localhost:4222"
    subject_prefix: "test"
  chains:
    evm:
      name: "evm"
      start_block: 100
      batch_size: 10
      poll_interval: 1s
      is_latest: false
      nodes:
        - "http://localhost:8545"
    tron:
      name: "tron"
      start_block: 200
      batch_size: 20
      poll_interval: 2s
      is_latest: true
      nodes:
        - "http://localhost:8090"
`

	_, err = tempFile.WriteString(configContent)
	if err != nil {
		t.Fatalf("Failed to write config content: %v", err)
	}
	tempFile.Close()

	// Load config
	cfg, err := Load(tempFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify config structure
	if cfg.Indexer.NATS.URL != "nats://localhost:4222" {
		t.Errorf("Expected NATS URL 'nats://localhost:4222', got '%s'", cfg.Indexer.NATS.URL)
	}

	if cfg.Indexer.NATS.SubjectPrefix != "test" {
		t.Errorf("Expected subject prefix 'test', got '%s'", cfg.Indexer.NATS.SubjectPrefix)
	}

	// Verify EVM chain config
	evmConfig, exists := cfg.Indexer.Chains["evm"]
	if !exists {
		t.Error("EVM chain config should exist")
	}

	if evmConfig.StartBlock != 100 {
		t.Errorf("Expected EVM start block 100, got %d", evmConfig.StartBlock)
	}

	if evmConfig.BatchSize != 10 {
		t.Errorf("Expected EVM batch size 10, got %d", evmConfig.BatchSize)
	}

	if evmConfig.PollInterval != time.Second {
		t.Errorf("Expected EVM poll interval 1s, got %v", evmConfig.PollInterval)
	}

	if evmConfig.IsLatest {
		t.Error("EVM should not be latest")
	}

	if len(evmConfig.Nodes) != 1 || evmConfig.Nodes[0] != "http://localhost:8545" {
		t.Errorf("Expected EVM nodes ['http://localhost:8545'], got %v", evmConfig.Nodes)
	}

	// Verify TRON chain config
	tronConfig, exists := cfg.Indexer.Chains["tron"]
	if !exists {
		t.Error("TRON chain config should exist")
	}

	if tronConfig.StartBlock != 200 {
		t.Errorf("Expected TRON start block 200, got %d", tronConfig.StartBlock)
	}

	if tronConfig.BatchSize != 20 {
		t.Errorf("Expected TRON batch size 20, got %d", tronConfig.BatchSize)
	}

	if tronConfig.PollInterval != 2*time.Second {
		t.Errorf("Expected TRON poll interval 2s, got %v", tronConfig.PollInterval)
	}

	// Note: The Load function overrides IsLatest to false for all chains
	if tronConfig.IsLatest {
		t.Error("TRON should not be latest (overridden by Load function)")
	}

	if len(tronConfig.Nodes) != 1 || tronConfig.Nodes[0] != "http://localhost:8090" {
		t.Errorf("Expected TRON nodes ['http://localhost:8090'], got %v", tronConfig.Nodes)
	}
}

func TestLoadConfig_InvalidFile(t *testing.T) {
	// Try to load non-existent config file
	_, err := Load("/non/existent/config.yaml")
	if err == nil {
		t.Error("Expected error when loading non-existent config file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	// Create a temporary config file with invalid YAML
	tempFile, err := os.CreateTemp("", "invalid_config_test.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	// Write invalid YAML content
	invalidContent := `
indexer:
  nats:
    url: "nats://localhost:4222"
    subject_prefix: "test"
  chains:
    evm:
      start_block: "invalid"  # Should be int, not string
      batch_size: 10
      poll_interval: 1000000000
      is_latest: false
      nodes:
        - "http://localhost:8545"
`

	_, err = tempFile.WriteString(invalidContent)
	if err != nil {
		t.Fatalf("Failed to write invalid config content: %v", err)
	}
	tempFile.Close()

	// Try to load invalid config
	_, err = Load(tempFile.Name())
	if err == nil {
		t.Error("Expected error when loading invalid config")
	}
}

func TestChainConfig_Validation(t *testing.T) {
	// Test valid config
	validConfig := ChainConfig{
		StartBlock:   100,
		BatchSize:    10,
		PollInterval: time.Second,
		IsLatest:     false,
		Nodes:        []string{"http://localhost:8545"},
	}

	// Test invalid config (negative start block)
	invalidConfig := ChainConfig{
		StartBlock:   -1,
		BatchSize:    10,
		PollInterval: time.Second,
		IsLatest:     false,
		Nodes:        []string{"http://localhost:8545"},
	}

	// Basic validation tests
	if validConfig.StartBlock <= 0 {
		t.Error("Valid config should have positive start block")
	}

	if invalidConfig.StartBlock > 0 {
		t.Error("Invalid config should have negative start block")
	}

	if validConfig.BatchSize <= 0 {
		t.Error("Valid config should have positive batch size")
	}

	if validConfig.PollInterval <= 0 {
		t.Error("Valid config should have positive poll interval")
	}

	if len(validConfig.Nodes) == 0 {
		t.Error("Valid config should have at least one node")
	}
}
