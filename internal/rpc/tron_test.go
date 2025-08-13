package rpc

import (
	"context"
	"testing"
)

func TestTronGetLatestBlockNumber(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddTronProvider("test", "https://tron-rpc.publicnode.com", nil, nil)
	err := fm.ExecuteTronCall(context.Background(), func(client *TronClient) error {
		blockNumber, err := client.GetNowBlock(context.Background())
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

func TestTronGetTransactionByHash(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.AddTronProvider("test", "https://tron-rpc.publicnode.com", nil, nil)

	err := fm.ExecuteTronCall(context.Background(), func(client *TronClient) error {
		tx, err := client.GetTransactionByID(context.Background(), "851282965e98efe44db2a35aee6e1a038e929c08da2793d1276faf5a7a42db66")
		if err != nil {
			return err
		}
		t.Logf("tx: %+v", tx)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteTronCall failed: %v", err)
	}
}
