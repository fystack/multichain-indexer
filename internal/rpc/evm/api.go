package evm

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

type FilterQuery struct {
	FromBlock string     // hex block number or "latest"
	ToBlock   string     // hex block number or "latest"
	Addresses []string   // contract addresses to filter
	Topics    [][]string // topics to filter (e.g., Transfer event signature)
}

type EthereumAPI interface {
	rpc.NetworkClient
	GetBlockNumber(ctx context.Context) (uint64, error)
	GetBlockByNumber(ctx context.Context, blockNumber string, detail bool) (*Block, error)
	FilterLogs(ctx context.Context, query FilterQuery) ([]Log, error)
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
