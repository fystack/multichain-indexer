package aptos

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

func NewAptosClient(
	url string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rateLimiter *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		BaseClient: rpc.NewBaseClient(
			url,
			rpc.NetworkAptos,
			rpc.ClientTypeREST,
			auth,
			timeout,
			rateLimiter,
		),
	}
}

func (c *Client) GetLedgerInfo(ctx context.Context) (*LedgerInfo, error) {
	data, err := c.Do(ctx, http.MethodGet, "/", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("getLedgerInfo failed: %w", err)
	}

	var info LedgerInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ledger info: %w", err)
	}
	return &info, nil
}

func (c *Client) GetBlockByVersion(ctx context.Context, version uint64, withTransactions bool) (*Block, error) {
	path := fmt.Sprintf("/blocks/by_version/%d", version)
	query := map[string]string{}
	if withTransactions {
		query["with_transactions"] = "true"
	}

	data, err := c.Do(ctx, http.MethodGet, path, nil, query)
	if err != nil {
		return nil, fmt.Errorf("getBlockByVersion failed: %w", err)
	}

	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &block, nil
}

func (c *Client) GetBlockByHeight(ctx context.Context, height uint64, withTransactions bool) (*Block, error) {
	path := fmt.Sprintf("/blocks/by_height/%d", height)
	query := map[string]string{}
	if withTransactions {
		query["with_transactions"] = "true"
	}

	data, err := c.Do(ctx, http.MethodGet, path, nil, query)
	if err != nil {
		return nil, fmt.Errorf("getBlockByHeight failed: %w", err)
	}

	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &block, nil
}

func (c *Client) GetTransactionsByVersion(ctx context.Context, start, limit uint64) ([]Transaction, error) {
	path := fmt.Sprintf("/transactions?start=%d&limit=%d", start, limit)

	data, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("getTransactionsByVersion failed: %w", err)
	}

	var txs []Transaction
	if err := json.Unmarshal(data, &txs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transactions: %w", err)
	}
	return txs, nil
}
