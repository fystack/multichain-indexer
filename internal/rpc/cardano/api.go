package cardano

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

// CardanoAPI defines the interface for Cardano RPC operations
type CardanoAPI interface {
	rpc.NetworkClient
	GetLatestBlockNumber(ctx context.Context) (uint64, error)
	GetBlockHeaderByNumber(ctx context.Context, blockNumber uint64) (*BlockResponse, error)
	GetBlockByNumber(ctx context.Context, blockNumber uint64) (*Block, error)
	GetBlockHash(ctx context.Context, blockNumber uint64) (string, error)
	GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]string, error)
	GetTransactionsByBlockHash(ctx context.Context, blockHash string) ([]string, error)
	GetTransaction(ctx context.Context, txHash string) (*Transaction, error)
	FetchTransactionsParallel(ctx context.Context, txHashes []string, concurrency int) ([]Transaction, error)
	GetBlockByHash(ctx context.Context, blockHash string) (*Block, error)
}

