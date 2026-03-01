package cosmos

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

type CosmosAPI interface {
	rpc.NetworkClient

	GetLatestHeight(ctx context.Context) (uint64, error)
	GetBlock(ctx context.Context, height uint64) (*BlockResponse, error)
	GetBlockResults(ctx context.Context, height uint64) (*BlockResultsResponse, error)
	GetTxByHash(ctx context.Context, hash string) (*TxResponse, error)
}
