package chains

import "github.com/fystack/indexer/internal/types"

type ChainIndexer interface {
	GetName() string
	GetLatestBlockNumber() (int64, error)
	GetBlock(number int64) (*types.Block, error)
	GetBlocks(from, to int64) ([]*types.Block, error)
	IsHealthy() bool
}
