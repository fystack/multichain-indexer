package tron

import (
	"context"

	"github.com/fystack/transaction-indexer/internal/rpc"
)

type TronAPI interface {
	rpc.NetworkClient
	GetBlockNumber(ctx context.Context) (uint64, error)
	GetBlockByNumber(ctx context.Context, blockNumber string, detail bool) (*Block, error)
	BatchGetTransactionReceiptsByBlockNum(ctx context.Context, blockNum int64) ([]*TxnInfo, error)
	GetTransactionInfo(ctx context.Context, txHash string) (*TxnInfo, error)
}
