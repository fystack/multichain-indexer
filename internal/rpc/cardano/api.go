package cardano

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

// CardanoAPI defines the interface for Cardano RPC operations
type CardanoAPI interface {
	rpc.NetworkClient
	GetLatestBlockNumber(ctx context.Context) (uint64, error)
	GetBlockByNumber(ctx context.Context, blockNumber uint64) (*Block, error)
	GetBlockHash(ctx context.Context, blockNumber uint64) (string, error)
	GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]string, error)
	GetTransaction(ctx context.Context, txHash string) (*Transaction, error)
	GetBlockByHash(ctx context.Context, blockHash string) (*Block, error)
}

