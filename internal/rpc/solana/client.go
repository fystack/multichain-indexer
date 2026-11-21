package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
func (c *Client) CallRPC(ctx context.Context, method string, params any) (*rpc.RPCResponse, error) {
	return c.base.CallRPC(ctx, method, params)
}

func (c *Client) Do(ctx context.Context, method, endpoint string, body any, params map[string]string) ([]byte, error) {
	return c.base.Do(ctx, method, endpoint, body, params)
}

func (c *Client) GetNetworkType() string { return c.base.GetNetworkType() }
func (c *Client) GetClientType() string  { return c.base.GetClientType() }
func (c *Client) GetURL() string         { return c.base.GetURL() }
func (c *Client) Close() error           { return c.base.Close() }

func (c *Client) GetSlot(ctx context.Context) (uint64, error) {
	resp, err := c.base.CallRPC(ctx, "getSlot", []any{map[string]any{"commitment": "finalized"}})
	if err != nil {
		return 0, err
	}
	var slot uint64
	if err := json.Unmarshal(resp.Result, &slot); err != nil {
		return 0, fmt.Errorf("decode getSlot result: %w", err)
	}
	return slot, nil
}

func (c *Client) GetTransaction(ctx context.Context, signature string) (*GetTransactionResult, error) {
	cfg := map[string]any{
		"encoding":                       "jsonParsed",
		"maxSupportedTransactionVersion": MaxSupportedTransactionVersion,
		"commitment":                     "finalized",
	}
	resp, err := c.base.CallRPC(ctx, "getTransaction", []any{signature, cfg})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("getTransaction RPC error: %w", resp.Error)
	}
	if string(resp.Result) == "null" {
		return nil, nil
	}
	var out GetTransactionResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode getTransaction result: %w", err)
	}
	return &out, nil
}

func (c *Client) GetBlock(ctx context.Context, slot uint64) (*GetBlockResult, error) {
	cfg := GetBlockConfig{
		Encoding:                       "jsonParsed",
		TransactionDetails:             "full",
		Rewards:                        false,
		MaxSupportedTransactionVersion: MaxSupportedTransactionVersion,
		Commitment:                     "finalized",
	}
	resp, err := c.base.CallRPC(ctx, "getBlock", []any{slot, cfg})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "-32007") || strings.Contains(strings.ToLower(msg), "was skipped") || strings.Contains(strings.ToLower(msg), "ledger jump") {
			return nil, nil
		}
		return nil, err
	}

	// Skipped slots are normal on Solana. Some RPCs return null, others return error -32007.
	if resp.Error != nil {
		if resp.Error.Code == -32007 {
			return nil, nil
		}
		return nil, fmt.Errorf("getBlock RPC error: %w", resp.Error)
	}
	if string(resp.Result) == "null" {
		return nil, nil
	}
	var out GetBlockResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode getBlock result: %w", err)
	}
	return &out, nil
}


func (c *Client) GetMultipleAccounts(ctx context.Context, pubkeys []string) (*GetMultipleAccountsResult, error) {
	cfg := map[string]any{"encoding": "base64", "commitment": "finalized"}
	resp, err := c.base.CallRPC(ctx, "getMultipleAccounts", []any{pubkeys, cfg})
	if err != nil {
		return nil, err
	}
	var out GetMultipleAccountsResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode getMultipleAccounts result: %w", err)
	}
	if len(out.Value) != len(pubkeys) {
		return nil, fmt.Errorf("getMultipleAccounts returned %d accounts for %d pubkeys", len(out.Value), len(pubkeys))
	}
	return &out, nil
}

// GetSignaturesForAddress returns finalized signatures touching address, newest first.
func (c *Client) GetSignaturesForAddress(ctx context.Context, address string, opts SignaturesOptions) ([]SignatureInfo, error) {
	cfg := map[string]any{"commitment": "finalized"}
	if opts.Before != "" {
		cfg["before"] = opts.Before
	}
	if opts.Until != "" {
		cfg["until"] = opts.Until
	}
	if opts.Limit > 0 {
		cfg["limit"] = opts.Limit
	}
	resp, err := c.base.CallRPC(ctx, "getSignaturesForAddress", []any{address, cfg})
	if err != nil {
		return nil, err
	}
	var out []SignatureInfo
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode getSignaturesForAddress result: %w", err)
	}
	return out, nil
}

// GetTokenAccountsByOwner returns the pubkeys of owner's token accounts under one token program.
func (c *Client) GetTokenAccountsByOwner(ctx context.Context, owner, programID string) ([]string, error) {
	cfg := map[string]any{"encoding": "base64", "commitment": "finalized", "dataSlice": map[string]any{"offset": 0, "length": 0}}
	resp, err := c.base.CallRPC(ctx, "getTokenAccountsByOwner", []any{owner, map[string]any{"programId": programID}, cfg})
	if err != nil {
		return nil, err
	}
	var out struct {
		Value []struct {
			Pubkey string `json:"pubkey"`
		} `json:"value"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode getTokenAccountsByOwner result: %w", err)
	}
	keys := make([]string, 0, len(out.Value))
	for _, v := range out.Value {
		keys = append(keys, v.Pubkey)
	}
	return keys, nil
}
