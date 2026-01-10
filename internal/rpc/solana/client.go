package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type Client struct {
	base *rpc.BaseClient
}

func NewSolanaClient(
	baseURL string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		base: rpc.NewBaseClient(baseURL, "sol", rpc.ClientTypeRPC, auth, timeout, rl),
	}
}

func (c *Client) GetSlot(ctx context.Context) (uint64, error) {
	resp, err := c.base.CallRPC(ctx, "getSlot", []any{"finalized"})
	if err != nil {
		return 0, err
	}
	var slot uint64
	if err := json.Unmarshal(resp.Result, &slot); err != nil {
		return 0, fmt.Errorf("decode getSlot result: %w", err)
	}
	return slot, nil
}

func (c *Client) GetBlock(ctx context.Context, slot uint64) (*GetBlockResult, error) {
	cfg := GetBlockConfig{
		Encoding:                       "jsonParsed",
		TransactionDetails:             "full",
		Rewards:                        false,
		MaxSupportedTransactionVersion: 0,
	}
	resp, err := c.base.CallRPC(ctx, "getBlock", []any{slot, cfg})
	if err != nil {
		return nil, err
	}
	// Some RPCs return null if slot is skipped.
	if string(resp.Result) == "null" {
		return nil, nil
	}
	var out GetBlockResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode getBlock result: %w", err)
	}
	return &out, nil
}

