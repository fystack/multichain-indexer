package aptos

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

// AptosAPI defines Aptos RPC methods used by the indexer.
type AptosAPI interface {
	rpc.NetworkClient

	GetLedgerInfo(ctx context.Context) (*LedgerInfo, error)
	GetLatestBlockHeight(ctx context.Context) (uint64, error)
	GetBlockByHeight(
		ctx context.Context,
		height uint64,
		withTransactions bool,
	) (*BlockResponse, error)
}
