package solana

import "context"

// SolanaAPI defines a minimal interface for our indexer's needs.
type SolanaAPI interface {
	GetSlot(ctx context.Context) (uint64, error)
	GetBlock(ctx context.Context, slot uint64) (*GetBlockResult, error)
}

