package chains

import "github.com/fystack/indexer/internal/types"

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
	Block  *types.Block
	Error  *Error // Nil if OK
}

type ChainIndexer interface {
	GetName() string
	GetLatestBlockNumber() (int64, error)
	GetBlock(number int64) (*types.Block, error)

	// batch version: each block can have its own error
	GetBlocks(from, to int64) ([]BlockResult, error)
	IsHealthy() bool
}
