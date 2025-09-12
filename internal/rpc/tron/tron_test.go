package tron

import (
	"context"
	"testing"
	"time"
)

func TestTronGetLatestBlockNumber(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewTronClient("https://tron-rpc.publicnode.com", nil, 10*time.Second, nil)
	blockNumber, err := client.GetBlockNumber(context.Background())
	if err != nil {
		t.Fatalf("GetBlockNumber failed: %v", err)
	}
	t.Logf("blockNumber: %d", blockNumber)
}

func TestTronGetBlockByNumber(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewTronClient("https://tron-rpc.publicnode.com", nil, 10*time.Second, nil)
	block, err := client.GetBlockByNumber(context.Background(), "", true)
	if err != nil {
		t.Fatalf("GetBlockByNumber failed: %v", err)
	}
	t.Logf("block: %+v", block)

}

func TestTronGetTransactionInfoByBlockNum(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewTronClient("https://api.trongrid.io", nil, 10*time.Second, nil)

	txs, err := client.BatchGetTransactionReceiptsByBlockNum(context.Background(), 49469984)
	if err != nil {
		t.Fatalf("BatchGetTransactionReceiptsByBlockNum failed: %v", err)
	}
	t.Logf("txs: %+v", txs)

}
