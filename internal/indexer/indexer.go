package indexer

import (
	"context"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
)

type Indexer interface {
	GetName() string
	GetNetworkType() enum.NetworkType
	GetNetworkInternalCode() string
	GetLatestBlockNumber(ctx context.Context) (uint64, error)
	GetBlock(ctx context.Context, number uint64) (*types.Block, error)

	// batch version: each block can have its own error
	GetBlocks(ctx context.Context, from, to uint64, isParallel bool) ([]BlockResult, error)
	GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error)
	IsHealthy() bool
}
