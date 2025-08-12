package indexer

import (
	"context"

	"idx/internal/core"
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
	Number int64 // Block number for debug
	Block  *core.Block
	Error  *Error // Nil if OK
}

type Indexer interface {
	GetName() string
	GetLatestBlockNumber(ctx context.Context) (int64, error)
	GetBlock(ctx context.Context, number int64) (*core.Block, error)

	// batch version: each block can have its own error
	GetBlocks(ctx context.Context, from, to int64) ([]BlockResult, error)
	IsHealthy() bool
}
