package xrp

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

type XRPLAPI interface {
	rpc.NetworkClient

	GetLatestLedgerIndex(ctx context.Context) (uint64, error)
	GetLedgerByIndex(ctx context.Context, ledgerIndex uint64) (*Ledger, error)
	BatchGetLedgersByIndex(ctx context.Context, ledgerIndexes []uint64) (map[uint64]*Ledger, error)
}
