package rpc

import (
	"context"
	"testing"
)

func TestGetLatestBlockNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)
	err := fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		blockNumber, err := client.GetBlockNumber(context.Background())
		if err != nil {
			return err
		}
		t.Logf("blockNumber: %d", blockNumber)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}

func TestGetBlockByNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)

	err := fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		block, err := client.GetBlockByNumber(context.Background(), "", true)
		if err != nil {
			return err
		}
		txnHashes := make([]string, 10)
		for i := range txnHashes {
			txnHashes[i] = block.Transactions[i].Hash
		}
		receipts, err := client.BatchGetTransactionReceipts(context.Background(), txnHashes)
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			t.Logf("receipt: %+v", receipt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}

func TestGetTransactionByHash(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)

	err := fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		tx, err := client.GetTransactionByHash(context.Background(), "0xddb0673450007337f13bf4b883bec15b27b0391ce1d661bb4589c8cfa954612f")
		if err != nil {
			return err
		}
		t.Logf("tx: %+v", tx)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}

func TestBatchGetBlocksByNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddEthereumProvider("test", "https://ethereum-rpc.publicnode.com", nil, nil)

	err := fm.ExecuteEthereumCall(context.Background(), func(client *EthereumClient) error {
		latestBlockNumber, err := client.GetBlockNumber(context.Background())
		if err != nil {
			return err
		}
		blocks, err := client.BatchGetBlocksByNumber(context.Background(), []uint64{latestBlockNumber - 10, latestBlockNumber - 9, latestBlockNumber - 8}, true)
		if err != nil {
			return err
		}
		for _, block := range blocks {
			t.Logf("block: %+v", block)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}
}
