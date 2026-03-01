package cosmos

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

func NewCosmosClient(
	baseURL string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		base: rpc.NewBaseClient(baseURL, rpc.NetworkCosmos, rpc.ClientTypeREST, auth, timeout, rl),
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

func (c *Client) GetLatestHeight(ctx context.Context) (uint64, error) {
	result, err := getResponse[StatusResponse](ctx, c, "/status", nil)
	if err != nil {
		return 0, err
	}
	height, err := strconv.ParseUint(result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid latest block height: %w", err)
	}
	return height, nil
}

func (c *Client) GetBlock(ctx context.Context, height uint64) (*BlockResponse, error) {
	return getResponse[BlockResponse](ctx, c, "/block", map[string]string{
		"height": strconv.FormatUint(height, 10),
	})
}

func (c *Client) GetBlockResults(
	ctx context.Context,
	height uint64,
) (*BlockResultsResponse, error) {
	return getResponse[BlockResultsResponse](ctx, c, "/block_results", map[string]string{
		"height": strconv.FormatUint(height, 10),
	})
}

func (c *Client) GetTxByHash(ctx context.Context, hash string) (*TxResponse, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil, fmt.Errorf("tx hash is empty")
	}
	if !strings.HasPrefix(hash, "0x") && !strings.HasPrefix(hash, "0X") {
		hash = "0x" + hash
	}

	return getResponse[TxResponse](ctx, c, "/tx", map[string]string{
		"hash":  hash,
		"prove": "false",
	})
}

func getResponse[T any](
	ctx context.Context,
	client *Client,
	endpoint string,
	params map[string]string,
) (*T, error) {
	raw, err := client.base.Do(ctx, http.MethodGet, endpoint, nil, params)
	if err != nil {
		return nil, err
	}

	var response rpc.RPCResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("%s failed: %w", endpoint, response.Error)
	}
	if len(response.Result) == 0 {
		return nil, fmt.Errorf("%s returned empty result", endpoint)
	}

	var result T
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return nil, fmt.Errorf("decode %s result: %w", endpoint, err)
	}
	return &result, nil
}
