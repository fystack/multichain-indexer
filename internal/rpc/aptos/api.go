package aptos

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

type AptosAPI interface {
	rpc.NetworkClient
	GetLedgerInfo(ctx context.Context) (*LedgerInfo, error)
	GetBlockByVersion(ctx context.Context, version uint64, withTransactions bool) (*Block, error)
	GetBlockByHeight(ctx context.Context, height uint64, withTransactions bool) (*Block, error)
	GetTransactionsByVersion(ctx context.Context, start, limit uint64) ([]Transaction, error)
}
