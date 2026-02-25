package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type Client struct {
	*rpc.BaseClient
}

func NewTronClient(
	url string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rateLimiter *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		BaseClient: rpc.NewBaseClient(
			url,
			rpc.NetworkTron,
			rpc.ClientTypeRPC,
			auth,
			timeout,
			rateLimiter,
		),
	}
}

// GetBlockNumber returns the current block number
func (t *Client) GetBlockNumber(ctx context.Context) (uint64, error) {
	data, err := t.Do(ctx, http.MethodPost, "/wallet/getnowblock", nil, nil)
	if err != nil {
		return 0, fmt.Errorf("getNowBlock failed: %w", err)
	}

	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return uint64(block.BlockHeader.RawData.Number), nil
}

// GetBlockByNumber returns a block with full transaction data
func (t *Client) GetBlockByNumber(
	ctx context.Context,
	blockNumber string,
	detail bool,
) (*Block, error) {
	body := map[string]any{
		"id_or_num": blockNumber,
		"detail":    detail,
	}
	data, err := t.Do(ctx, http.MethodPost, "/wallet/getblock", body, nil)
	if err != nil {
		return nil, fmt.Errorf("getBlockByNumber failed: %w", err)
	}
	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &block, nil
}

func (t *Client) BatchGetTransactionReceiptsByBlockNum(
	ctx context.Context,
	blockNum int64,
) ([]*TxnInfo, error) {
	body := map[string]any{"num": blockNum}
	data, err := t.Do(
		ctx,
		http.MethodPost,
		"/wallet/gettransactioninfobyblocknum",
		body,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("getTransactionInfoByBlockNum failed: %w", err)
	}
	var txs []*TxnInfo
	if err := json.Unmarshal(data, &txs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transactions: %w", err)
	}
	return txs, nil
}

// GetTransactionInfo gets detailed transaction info including fees
func (t *Client) GetTransactionInfo(ctx context.Context, txHash string) (*TxnInfo, error) {
	body := map[string]any{"value": txHash}
	data, err := t.Do(ctx, http.MethodPost, "/wallet/gettransactioninfobyid", body, nil)
	if err != nil {
		return nil, fmt.Errorf("getTransactionInfoById failed: %w", err)
	}
	var txInfo TxnInfo
	if err := json.Unmarshal(data, &txInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction info: %w", err)
	}
	return &txInfo, nil
}
