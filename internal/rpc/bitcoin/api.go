package bitcoin

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

// BitcoinAPI defines the Bitcoin RPC interface
type BitcoinAPI interface {
	rpc.NetworkClient

	// Block operations
	GetBlockCount(ctx context.Context) (uint64, error)
	GetBlockHash(ctx context.Context, height uint64) (string, error)
	GetBlock(ctx context.Context, hash string, verbosity int) (*Block, error)
	GetBlockByHeight(ctx context.Context, height uint64, verbosity int) (*Block, error)

	// Network info
	GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error)

	// Mempool operations
	GetRawMempool(ctx context.Context, verbose bool) (interface{}, error)
	GetRawTransaction(ctx context.Context, txid string, verbose bool) (*Transaction, error)
	GetTransactionWithPrevouts(ctx context.Context, txid string) (*Transaction, error)
	GetMempoolEntry(ctx context.Context, txid string) (*MempoolEntry, error)
}
