package evm

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

type EthereumAPI interface {
	rpc.NetworkClient
	GetBlockNumber(ctx context.Context) (uint64, error)
	GetBlockByNumber(ctx context.Context, blockNumber string, detail bool) (*Block, error)
	BatchGetBlocksByNumber(
		ctx context.Context,
		blockNumbers []uint64,
		fullTxs bool,
	) (map[uint64]*Block, error)
	BatchGetTransactionReceipts(
		ctx context.Context,
		txHashes []string,
	) (map[string]*TxnReceipt, error)
}
