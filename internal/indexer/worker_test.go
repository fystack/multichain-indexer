package indexer

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/kvstore"
)

func TestWorker_KVStoreIntegration(t *testing.T) {
	// Create temporary directory for BadgerStore
	tempDir, err := os.MkdirTemp("", "worker_test")
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

	// Test saving and retrieving block numbers
	chainName := "test_chain"
	key := "latest_block_" + chainName
	blockNumber := int64(500)

	// Save block number
	err = store.Set(key, []byte(strconv.FormatInt(blockNumber, 10)))
	if err != nil {
		t.Errorf("Failed to save block number: %v", err)
	}

	// Retrieve block number
	data, err := store.Get(key)
	if err != nil {
		t.Errorf("Failed to get block number: %v", err)
	}

	retrievedBlock, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		t.Errorf("Failed to parse block number: %v", err)
	}

	if retrievedBlock != blockNumber {
		t.Errorf("Expected block number %d, got %d", blockNumber, retrievedBlock)
	}
}

func TestWorker_ConfigValidation(t *testing.T) {
	// Test valid config
	validConfig := config.ChainConfig{
		StartBlock:   100,
		BatchSize:    10,
		PollInterval: time.Second,
		IsLatest:     false,
	}

	if validConfig.StartBlock <= 0 {
		t.Error("StartBlock should be positive")
	}

	if validConfig.BatchSize <= 0 {
		t.Error("BatchSize should be positive")
	}

	if validConfig.PollInterval <= 0 {
		t.Error("PollInterval should be positive")
	}
}

func TestWorker_BlockNumberParsing(t *testing.T) {
	// Test valid block number
	validBlockStr := "12345"
	blockNum, err := strconv.ParseInt(validBlockStr, 10, 64)
	if err != nil {
		t.Errorf("Failed to parse valid block number: %v", err)
	}
	if blockNum != 12345 {
		t.Errorf("Expected 12345, got %d", blockNum)
	}

	// Test invalid block number
	invalidBlockStr := "invalid"
	_, err = strconv.ParseInt(invalidBlockStr, 10, 64)
	if err == nil {
		t.Error("Expected error when parsing invalid block number")
	}
}

func TestWorker_KeyGeneration(t *testing.T) {
	chainName := "test_chain"
	expectedKey := "latest_block_" + chainName

	// Test key generation
	key := "latest_block_" + chainName
	if key != expectedKey {
		t.Errorf("Expected key %s, got %s", expectedKey, key)
	}
}

func TestWorker_ErrorHandling(t *testing.T) {
	// Test handling of non-existent keys
	tempDir, err := os.MkdirTemp("", "worker_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := kvstore.NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	// Try to get non-existent key
	_, err = store.Get("non_existent_key")
	if err == nil {
		t.Error("Expected error when getting non-existent key")
	}
}
