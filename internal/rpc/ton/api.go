package ton

import (
	"context"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
)

type TonAPI interface {
	// ListTransactions returns transactions for an account.
	// - limit: max transactions to return (typically 10-50)
	// - lastLT: logical time cursor (0 for initial fetch from beginning)
	// - lastHash: transaction hash cursor (nil for initial fetch)
	// Returns transactions in reverse chronological order (newest first).
	ListTransactions(ctx context.Context, addr *address.Address, limit uint32, lastLT uint64, lastHash []byte) ([]*tlb.Transaction, error)

	// GetLatestMasterchainSeqno returns the latest observed masterchain sequence number.
	GetLatestMasterchainSeqno(ctx context.Context) (uint64, error)

	// ResolveJettonMasterAddress resolves a jetton wallet address to its master contract address.
	ResolveJettonMasterAddress(ctx context.Context, jettonWallet string) (string, error)
}
