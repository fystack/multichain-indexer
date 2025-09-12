package evm

import (
	"context"
	"testing"
	"time"
)

func TestGetLatestBlockNumber(t *testing.T) {
	ctx := context.Background()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewEthereumClient("https://ethereum-rpc.publicnode.com", nil, 10*time.Second, nil)

	blockNumber, err := client.GetBlockNumber(ctx)
	if err != nil {
		t.Fatalf("GetBlockNumber failed: %v", err)
	}
	if blockNumber == 0 {
		t.Error("Expected non-zero block number")
	}
	t.Logf("Latest block number: %d", blockNumber)
}

func TestGetBlockByNumber(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewEthereumClient("https://ethereum-rpc.publicnode.com", nil, 10*time.Second, nil)

	// Get latest block with full transactions
	block, err := client.GetBlockByNumber(context.Background(), "latest", true)
	if err != nil {
		t.Fatalf("GetBlockByNumber failed: %v", err)
	}

	t.Logf("Block number: %s", block.Number)
	t.Logf("Block hash: %s", block.Hash)
	t.Logf("Block timestamp: %s", block.Timestamp)
	t.Logf("Number of transactions: %d", len(block.Transactions))

	// Test with first few transactions if available
	if len(block.Transactions) > 0 {
		txCount := len(block.Transactions)
		if txCount > 5 {
			txCount = 5 // Limit to first 5 for testing
		}

		txnHashes := make([]string, txCount)
		for i := 0; i < txCount; i++ {
			txnHashes[i] = block.Transactions[i].Hash
			t.Logf("Transaction %d: %s", i, block.Transactions[i].Hash)
		}

		// Get receipts for transaction fee calculation
		receipts, err := client.BatchGetTransactionReceipts(context.Background(), txnHashes)
		if err != nil {
			t.Fatalf("BatchGetTransactionReceipts failed: %v", err)
		}

		t.Logf("Retrieved %d receipts", len(receipts))
		for hash, receipt := range receipts {
			t.Logf("Receipt for %s: gasUsed=%s, effectiveGasPrice=%s",
				hash, receipt.GasUsed, receipt.EffectiveGasPrice)
		}
	}
}

func TestBatchGetBlocksByNumber(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewEthereumClient("https://ethereum-rpc.publicnode.com", nil, 10*time.Second, nil)

	latestBlockNumber, err := client.GetBlockNumber(context.Background())
	if err != nil {
		t.Fatalf("GetBlockNumber failed: %v", err)
	}

	// Test batch get with recent blocks
	testBlocks := []uint64{
		latestBlockNumber - 5,
		latestBlockNumber - 4,
		latestBlockNumber - 3,
	}

	blocks, err := client.BatchGetBlocksByNumber(context.Background(), testBlocks, true)
	if err != nil {
		t.Fatalf("BatchGetBlocksByNumber failed: %v", err)
	}

	t.Logf("Retrieved %d blocks via batch call", len(blocks))

	for blockNum, block := range blocks {
		t.Logf("Block %d: hash=%s, txCount=%d, timestamp=%s",
			blockNum, block.Hash, len(block.Transactions), block.Timestamp)
	}
}
