package rpc

import (
	"context"
	"fmt"
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
		fmt.Println(blockNumber)
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
		block, err := client.GetBlockByNumber(context.Background(), "latest", true)
		if err != nil {
			return err
		}
		fmt.Println(block.Transactions[0].Hash)
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
		fmt.Println(tx)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteEthereumCall failed: %v", err)
	}

}
