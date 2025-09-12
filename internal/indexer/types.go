package indexer

import "github.com/fystack/transaction-indexer/pkg/common/types"

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
