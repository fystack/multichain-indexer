package rpc

import (
	"context"
	"testing"
)

func TestGetLatestBlockNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	err := fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)
	if err != nil {
		t.Fatalf("Failed to add provider: %v", err)
	}

	err = fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		blockNumber, err := client.GetBlockNumber(context.Background())
		if err != nil {
			return err
		}
		t.Logf("Latest block number: %d", blockNumber)
		return nil
	})

	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}

func TestGetBlockByNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	err := fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)
	if err != nil {
		t.Fatalf("Failed to add provider: %v", err)
	}

	err = fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		// Get latest block with full transactions
		block, err := client.GetBlockByNumber(context.Background(), "latest", true)
		if err != nil {
			return err
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
				return err
			}

			t.Logf("Retrieved %d receipts", len(receipts))
			for hash, receipt := range receipts {
				t.Logf("Receipt for %s: gasUsed=%s, effectiveGasPrice=%s",
					hash, receipt.GasUsed, receipt.EffectiveGasPrice)
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}

func TestBatchGetBlocksByNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	err := fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)
	if err != nil {
		t.Fatalf("Failed to add provider: %v", err)
	}

	err = fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		latestBlockNumber, err := client.GetBlockNumber(context.Background())
		if err != nil {
			return err
		}

		// Test batch get with recent blocks
		testBlocks := []uint64{
			latestBlockNumber - 5,
			latestBlockNumber - 4,
			latestBlockNumber - 3,
		}

		blocks, err := client.BatchGetBlocksByNumber(context.Background(), testBlocks, true)
		if err != nil {
			return err
		}

		t.Logf("Retrieved %d blocks via batch call", len(blocks))

		for blockNum, block := range blocks {
			t.Logf("Block %d: hash=%s, txCount=%d, timestamp=%s",
				blockNum, block.Hash, len(block.Transactions), block.Timestamp)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}

func TestMultipleProviders(t *testing.T) {
	fm := NewFailoverManager(nil)

	// Add multiple providers for failover testing
	err := fm.AddEthereumProvider("publicnode", "https://ethereum-rpc.publicnode.com", nil, nil)
	if err != nil {
		t.Fatalf("Failed to add publicnode provider: %v", err)
	}

	err = fm.AddEthereumProvider("cloudflare", "https://cloudflare-eth.com", nil, nil)
	if err != nil {
		t.Fatalf("Failed to add cloudflare provider: %v", err)
	}

	err = fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		blockNumber, err := client.GetBlockNumber(context.Background())
		if err != nil {
			return err
		}

		t.Logf("Successfully got block number %d with failover setup", blockNumber)
		return nil
	})

	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}
