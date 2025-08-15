package kvstore

import (
	"errors"
	"os"
	"testing"
)

func TestFailedBlockStore(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "failed_block_store_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create BadgerStore
	badgerStore, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer badgerStore.Close()

	// Create FailedBlockStore
	fbs := NewFailedBlockStore(badgerStore)

	// Test storing a failed block
	chain := "ethereum"
	blockNumber := uint64(12345)
	testError := errors.New("RPC timeout")

	err = fbs.StoreFailedBlock(chain, blockNumber, testError)
	if err != nil {
		t.Fatalf("Failed to store failed block: %v", err)
	}

	// Test retrieving the failed block
	info, err := fbs.GetFailedBlock(chain, blockNumber)
	if err != nil {
		t.Fatalf("Failed to get failed block: %v", err)
	}

	if info == nil {
		t.Fatal("Expected failed block info, got nil")
	}

	if info.BlockNumber != blockNumber {
		t.Errorf("Expected block number %d, got %d", blockNumber, info.BlockNumber)
	}

	if info.Chain != chain {
		t.Errorf("Expected chain %s, got %s", chain, info.Chain)
	}

	if info.Error != testError.Error() {
		t.Errorf("Expected error %s, got %s", testError.Error(), info.Error)
	}

	if info.RetryCount != 1 {
		t.Errorf("Expected retry count 1, got %d", info.RetryCount)
	}

	if info.Resolved {
		t.Error("Expected resolved to be false")
	}

	// Test getting failed blocks list
	failedBlocks, err := fbs.GetFailedBlocks(chain)
	if err != nil {
		t.Fatalf("Failed to get failed blocks list: %v", err)
	}

	if len(failedBlocks) != 1 {
		t.Errorf("Expected 1 failed block, got %d", len(failedBlocks))
	}

	if failedBlocks[0] != blockNumber {
		t.Errorf("Expected failed block %d, got %d", blockNumber, failedBlocks[0])
	}

	// Test storing the same block again (should increment retry count)
	err = fbs.StoreFailedBlock(chain, blockNumber, testError)
	if err != nil {
		t.Fatalf("Failed to store failed block again: %v", err)
	}

	info, err = fbs.GetFailedBlock(chain, blockNumber)
	if err != nil {
		t.Fatalf("Failed to get failed block after retry: %v", err)
	}

	if info.RetryCount != 2 {
		t.Errorf("Expected retry count 2, got %d", info.RetryCount)
	}

	// Test resolving the failed block
	err = fbs.ResolveFailedBlock(chain, blockNumber)
	if err != nil {
		t.Fatalf("Failed to resolve failed block: %v", err)
	}

	info, err = fbs.GetFailedBlock(chain, blockNumber)
	if err != nil {
		t.Fatalf("Failed to get resolved block: %v", err)
	}

	if !info.Resolved {
		t.Error("Expected resolved to be true")
	}

	// Test getting failed blocks list after resolving (should be empty)
	failedBlocks, err = fbs.GetFailedBlocks(chain)
	if err != nil {
		t.Fatalf("Failed to get failed blocks list after resolving: %v", err)
	}

	if len(failedBlocks) != 0 {
		t.Errorf("Expected 0 failed blocks after resolving, got %d", len(failedBlocks))
	}

	// Test getting all failed blocks (including resolved)
	allFailedBlocks, err := fbs.GetAllFailedBlocks(chain)
	if err != nil {
		t.Fatalf("Failed to get all failed blocks: %v", err)
	}

	if len(allFailedBlocks) != 1 {
		t.Errorf("Expected 1 total failed block, got %d", len(allFailedBlocks))
	}

	if !allFailedBlocks[0].Resolved {
		t.Error("Expected the block to be resolved")
	}

	// Test cleanup of resolved blocks
	err = fbs.CleanupResolvedBlocks(chain, 0) // Clean up all resolved blocks
	if err != nil {
		t.Fatalf("Failed to cleanup resolved blocks: %v", err)
	}

	// After cleanup, should have no blocks
	allFailedBlocks, err = fbs.GetAllFailedBlocks(chain)
	if err != nil {
		t.Fatalf("Failed to get all failed blocks after cleanup: %v", err)
	}

	if len(allFailedBlocks) != 0 {
		t.Errorf("Expected 0 total failed blocks after cleanup, got %d", len(allFailedBlocks))
	}
}

func TestFailedBlockStoreMultipleChains(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "failed_block_store_multi_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create BadgerStore
	badgerStore, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer badgerStore.Close()

	// Create FailedBlockStore
	fbs := NewFailedBlockStore(badgerStore)

	// Test storing failed blocks for different chains
	chains := []string{"ethereum", "bitcoin", "tron"}
	for i, chain := range chains {
		blockNumber := uint64(100 + i)
		testError := errors.New("test error for " + chain)

		err = fbs.StoreFailedBlock(chain, blockNumber, testError)
		if err != nil {
			t.Fatalf("Failed to store failed block for chain %s: %v", chain, err)
		}
	}

	// Test that each chain has its own failed blocks list
	for i, chain := range chains {
		failedBlocks, err := fbs.GetFailedBlocks(chain)
		if err != nil {
			t.Fatalf("Failed to get failed blocks for chain %s: %v", chain, err)
		}

		if len(failedBlocks) != 1 {
			t.Errorf("Expected 1 failed block for chain %s, got %d", chain, len(failedBlocks))
		}

		expectedBlockNumber := uint64(100 + i)
		if failedBlocks[0] != expectedBlockNumber {
			t.Errorf("Expected failed block %d for chain %s, got %d", expectedBlockNumber, chain, failedBlocks[0])
		}
	}
}
