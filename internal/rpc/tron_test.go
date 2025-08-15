package rpc

import (
	"context"
	"testing"
)

func TestTronGetLatestBlockNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddTronProvider("test", "https://tron-rpc.publicnode.com", nil, nil)
	err := fm.ExecuteTronCall(context.Background(), func(client *TronClient) error {
		blockNumber, err := client.GetBlockNumber(context.Background())
		if err != nil {
			return err
		}
		t.Logf("blockNumber: %d", blockNumber)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteTronCall failed: %v", err)
	}
}

func TestTronGetBlockByNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddTronProvider("test", "https://tron-rpc.publicnode.com", nil, nil)

	err := fm.ExecuteTronCall(context.Background(), func(client *TronClient) error {
		block, err := client.GetBlockByNumber(context.Background(), "", true)
		if err != nil {
			return err
		}
		t.Logf("block: %+v", block)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteTronCall failed: %v", err)
	}
}

func TestTronGetTransactionInfoByBlockNum(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddTronProvider("test", "https://api.trongrid.io", nil, nil)

	err := fm.ExecuteTronCall(context.Background(), func(client *TronClient) error {
		txs, err := client.BatchGetTransactionReceiptsByBlockNum(context.Background(), 49469984)
		if err != nil {
			return err
		}
		t.Logf("txs: %+v", txs)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteTronCall failed: %v", err)
	}
}
