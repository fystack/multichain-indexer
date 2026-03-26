package aptos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type Client struct {
	base *rpc.BaseClient
}

func NewAptosClient(
	baseURL string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		base: rpc.NewBaseClient(baseURL, rpc.NetworkAptos, rpc.ClientTypeREST, auth, timeout, rl),
	}
}

func (c *Client) CallRPC(ctx context.Context, method string, params any) (*rpc.RPCResponse, error) {
	return c.base.CallRPC(ctx, method, params)
}

func (c *Client) Do(
	ctx context.Context,
	method, endpoint string,
	body any,
	params map[string]string,
) ([]byte, error) {
	return c.base.Do(ctx, method, endpoint, body, params)
}

func (c *Client) GetNetworkType() string { return c.base.GetNetworkType() }
func (c *Client) GetClientType() string  { return c.base.GetClientType() }
func (c *Client) GetURL() string         { return c.base.GetURL() }
func (c *Client) Close() error           { return c.base.Close() }

func (c *Client) endpoint(path string) string {
	base := strings.TrimSuffix(c.base.GetURL(), "/")
	path = "/" + strings.TrimPrefix(path, "/")

	if strings.HasSuffix(base, "/v1") {
		trimmed := strings.TrimPrefix(path, "/v1")
		if trimmed == "" {
			return ""
		}
		return trimmed
	}
	return path
}

func (c *Client) GetLedgerInfo(ctx context.Context) (*LedgerInfo, error) {
	raw, err := c.base.Do(ctx, http.MethodGet, c.endpoint("/v1"), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get ledger info failed: %w", err)
	}

	var info LedgerInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("decode ledger info failed: %w", err)
	}
	return &info, nil
}

func (c *Client) GetLatestBlockHeight(ctx context.Context) (uint64, error) {
	info, err := c.GetLedgerInfo(ctx)
	if err != nil {
		return 0, err
	}

	height, err := strconv.ParseUint(strings.TrimSpace(info.BlockHeight), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid block_height %q: %w", info.BlockHeight, err)
	}
	return height, nil
}

func (c *Client) GetBlockByHeight(
	ctx context.Context,
	height uint64,
	withTransactions bool,
) (*BlockResponse, error) {
	endpoint := c.endpoint(fmt.Sprintf("/v1/blocks/by_height/%d", height))
	params := map[string]string{"with_transactions": strconv.FormatBool(withTransactions)}

	raw, err := c.base.Do(ctx, http.MethodGet, endpoint, nil, params)
	if err != nil {
		return nil, fmt.Errorf("get block by height %d failed: %w", height, err)
	}

	var block BlockResponse
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, fmt.Errorf("decode block by height %d failed: %w", height, err)
	}
	return &block, nil
}

func (c *Client) GetBlockByVersion(
	ctx context.Context,
	version uint64,
	withTransactions bool,
) (*BlockResponse, error) {
	endpoint := c.endpoint(fmt.Sprintf("/v1/blocks/by_version/%d", version))
	params := map[string]string{"with_transactions": strconv.FormatBool(withTransactions)}

	raw, err := c.base.Do(ctx, http.MethodGet, endpoint, nil, params)
	if err != nil {
		return nil, fmt.Errorf("get block by version %d failed: %w", version, err)
	}

	var block BlockResponse
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, fmt.Errorf("decode block by version %d failed: %w", version, err)
	}
	return &block, nil
}

func (c *Client) GetTransactionByHash(
	ctx context.Context,
	hash string,
) (*Transaction, error) {
	endpoint := c.endpoint(fmt.Sprintf("/v1/transactions/by_hash/%s", strings.TrimSpace(hash)))

	raw, err := c.base.Do(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get transaction by hash %s failed: %w", hash, err)
	}

	var tx Transaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("decode transaction by hash %s failed: %w", hash, err)
	}
	return &tx, nil
}

func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 404") || strings.Contains(msg, "not found")
}
