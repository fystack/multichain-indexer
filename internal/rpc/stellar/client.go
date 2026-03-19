package stellar

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

func NewClient(
	baseURL string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		base: rpc.NewBaseClient(baseURL, rpc.NetworkStellar, rpc.ClientTypeREST, auth, timeout, rl),
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
func (c *Client) SetCustomHeaders(headers map[string]string) {
	c.base.SetCustomHeaders(headers)
}

func (c *Client) GetLatestLedgerSequence(ctx context.Context) (uint64, error) {
	raw, err := c.base.Do(ctx, http.MethodGet, "", nil, nil)
	if err != nil {
		return 0, fmt.Errorf("get stellar root failed: %w", err)
	}

	var root Root
	if err := json.Unmarshal(raw, &root); err != nil {
		return 0, fmt.Errorf("decode stellar root failed: %w", err)
	}
	if root.HistoryLatestLedger == 0 {
		return 0, fmt.Errorf("stellar root returned empty history_latest_ledger")
	}
	return root.HistoryLatestLedger, nil
}

func (c *Client) GetLedger(ctx context.Context, sequence uint64) (*Ledger, error) {
	raw, err := c.base.Do(ctx, http.MethodGet, fmt.Sprintf("/ledgers/%d", sequence), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get stellar ledger %d failed: %w", sequence, err)
	}

	var ledger Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return nil, fmt.Errorf("decode stellar ledger %d failed: %w", sequence, err)
	}
	return &ledger, nil
}

func (c *Client) GetPaymentsByLedger(ctx context.Context, sequence uint64, cursor string, limit int) (*PaymentsPage, error) {
	if limit <= 0 {
		limit = 200
	}
	params := map[string]string{
		"order": "asc",
		"limit": strconv.Itoa(limit),
	}
	if strings.TrimSpace(cursor) != "" {
		params["cursor"] = cursor
	}

	raw, err := c.base.Do(ctx, http.MethodGet, fmt.Sprintf("/ledgers/%d/payments", sequence), nil, params)
	if err != nil {
		return nil, fmt.Errorf("get stellar payments for ledger %d failed: %w", sequence, err)
	}

	var page PaymentsPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decode stellar payments for ledger %d failed: %w", sequence, err)
	}
	return &page, nil
}

func (c *Client) GetTransaction(ctx context.Context, hash string) (*Transaction, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil, fmt.Errorf("stellar transaction hash is empty")
	}

	raw, err := c.base.Do(ctx, http.MethodGet, fmt.Sprintf("/transactions/%s", hash), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get stellar transaction %s failed: %w", hash, err)
	}

	var tx Transaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("decode stellar transaction %s failed: %w", hash, err)
	}
	return &tx, nil
}
