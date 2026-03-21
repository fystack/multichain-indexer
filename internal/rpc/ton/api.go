package ton

import (
	"context"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	tonlib "github.com/xssnick/tonutils-go/ton"
)

// TonAPI defines TON RPC operations used by the TON indexer.
type TonAPI interface {
	StickyContext(ctx context.Context) context.Context
	GetLatestMasterchainInfo(ctx context.Context) (*tonlib.BlockIDExt, error)
	LookupMasterchainBlock(ctx context.Context, seqno uint32) (*tonlib.BlockIDExt, error)
	LookupBlock(ctx context.Context, workchain int32, shard int64, seqno uint32) (*tonlib.BlockIDExt, error)
	GetBlockData(ctx context.Context, block *tonlib.BlockIDExt) (*tlb.Block, error)
	GetBlockShardsInfo(ctx context.Context, master *tonlib.BlockIDExt) ([]*tonlib.BlockIDExt, error)
	GetBlockTransactionsV2(
		ctx context.Context,
		masterSeqno uint32,
		block *tonlib.BlockIDExt,
		count uint32,
		after *tonlib.TransactionID3,
	) ([]tonlib.TransactionShortInfo, bool, error)
	GetTransaction(
		ctx context.Context,
		block *tonlib.BlockIDExt,
		addr *address.Address,
		lt uint64,
	) (*tlb.Transaction, error)
	GetAccount(ctx context.Context, block *tonlib.BlockIDExt, addr *address.Address) (*tlb.Account, error)
	ListTransactions(ctx context.Context, addr *address.Address, num uint32, lt uint64, txHash []byte) ([]*tlb.Transaction, error)
	ResolveJettonWalletAddress(
		ctx context.Context,
		jettonMaster string,
		ownerAddress string,
	) (wallet string, err error)
	ResolveJettonWalletData(ctx context.Context, jettonWallet string) (owner string, master string, err error)
	ResolveJettonMasterAddress(ctx context.Context, jettonWallet string) (string, error)
	Close() error
}
