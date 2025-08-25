package indexer

import (
	"context"

	"github.com/fystack/transaction-indexer/pkg/common/types"
)

type ErrorType string

const (
	ErrorTypeBlockUnmarshal ErrorType = "block_unmarshal"
	ErrorTypeBlockNotFound  ErrorType = "block_not_found"
	ErrorTypeBlockNil       ErrorType = "block_nil"
	ErrorTypeTimeout        ErrorType = "timeout"
	ErrorTypeUnknown        ErrorType = "unknown"
)

type Error struct {
	ErrorType ErrorType
	Message   string
}

type BlockResult struct {
	Number uint64 // Block number for debug
	Block  *types.Block
	Error  *Error // Nil if OK
}

type Indexer interface {
	GetName() string
	GetLatestBlockNumber(ctx context.Context) (uint64, error)
	GetBlock(ctx context.Context, number uint64) (*types.Block, error)

	// batch version: each block can have its own error
	GetBlocks(ctx context.Context, from, to uint64) ([]BlockResult, error)
	GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error)
	IsHealthy() bool
}
