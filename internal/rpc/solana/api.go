package solana

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

// SolanaAPI is the concrete client interface used by Failover.
// It must satisfy rpc.NetworkClient due to Failover[T NetworkClient].
type SolanaAPI interface {
	rpc.NetworkClient

	GetSlot(ctx context.Context) (uint64, error)
	GetBlock(ctx context.Context, slot uint64) (*GetBlockResult, error)
	GetTransaction(ctx context.Context, signature string) (*GetTransactionResult, error)

	// Account watching (watch_mode: accounts).
	GetMultipleAccounts(ctx context.Context, pubkeys []string) (*GetMultipleAccountsResult, error)
	GetSignaturesForAddress(ctx context.Context, address string, opts SignaturesOptions) ([]SignatureInfo, error)
	GetTokenAccountsByOwner(ctx context.Context, owner, programID string) ([]string, error)
}

