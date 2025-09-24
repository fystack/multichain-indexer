package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type NetworkClient interface {
	CallRPC(ctx context.Context, method string, params any) (*RPCResponse, error)
	Do(
		ctx context.Context,
		method, endpoint string,
		body any,
		params map[string]string,
	) ([]byte, error)
	GetNetworkType() string
	GetClientType() string
	GetURL() string
	Close() error
}

type BaseClient struct {
	baseURL     string
	network     string
	clientType  string
	rpcID       int64
	httpClient  *http.Client
	auth        *AuthConfig
	rateLimiter *ratelimiter.PooledRateLimiter
	mu          sync.Mutex
}

func NewBaseClient(
	baseURL, network, clientType string,
	auth *AuthConfig,
	timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter,
) *BaseClient {
	return &BaseClient{
		httpClient:  &http.Client{Timeout: timeout},
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		auth:        auth,
		network:     network,
		clientType:  clientType,
		rateLimiter: rl,
		rpcID:       1,
	}
}

func (c *BaseClient) CallRPC(ctx context.Context, method string, params any) (*RPCResponse, error) {
	if c.clientType != ClientTypeRPC {
		return nil, fmt.Errorf("client is %s, not RPC", c.clientType)
	}
	c.mu.Lock()
	id := c.rpcID
	c.rpcID++
	c.mu.Unlock()

	req := &RPCRequest{ID: id, JSONRPC: "2.0", Method: method, Params: params}
	raw, err := c.Do(ctx, http.MethodPost, "", req, nil)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", method, err)
	}

	var resp RPCResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%s decode error: %w (response: %s)", method, err, string(raw))
	}
	if resp.Error != nil {
		return &resp, fmt.Errorf("%s RPC error: %w", method, resp.Error)
	}
	return &resp, nil
}

func (c *BaseClient) doRaw(
	ctx context.Context,
	method, endpoint string,
	body any,
	params map[string]string,
) ([]byte, error) {
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, c.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	u, err := url.Parse(c.baseURL + endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body failed: %w", err)
		}
		r = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, fmt.Errorf("new request failed: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.auth != nil {
		switch c.auth.Type {
		case AuthTypeHeader:
			req.Header.Set(c.auth.Key, c.auth.Value)
		case AuthTypeQuery:
			q := req.URL.Query()
			q.Set(c.auth.Key, c.auth.Value)
			req.URL.RawQuery = q.Encode()
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return data, fmt.Errorf("HTTP %d: failed to read response body: %w", resp.StatusCode, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("HTTP %s: %s",
			resp.Status, string(data))
	}

	if len(data) == 0 {
		return data, fmt.Errorf("HTTP %d: empty response body", resp.StatusCode)
	}

	return data, nil
}

func (c *BaseClient) Do(
	ctx context.Context,
	method, endpoint string,
	body any,
	params map[string]string,
) ([]byte, error) {
	return c.doRaw(ctx, method, endpoint, body, params)
}

// DoBatch executes a JSON-RPC batch request.
//
// It marshals the provided slice of RPCRequest into a single HTTP POST request,
// applies rate limiting and authentication like Do(), then parses the response
// into a slice of RPCResponse.
//
// This is specific to JSON-RPC batch calls (e.g. eth_getBlockByNumber on Ethereum),
// and should not be used for generic HTTP/REST endpoints.
//
// Example:
//
//	requests := []*rpc.RPCRequest{
//	    {ID: 1, JSONRPC: "2.0", Method: "eth_blockNumber", Params: nil},
//	    {ID: 2, JSONRPC: "2.0", Method: "eth_getBlockByNumber", Params: []any{"latest", true}},
//	}
//
//	responses, err := client.DoBatch(ctx, requests)
//	if err != nil { ... }
//
//	for _, r := range responses {
//	    if r.Error != nil { ... }
//	    fmt.Println("result:", string(r.Result))
//	}
func (c *BaseClient) DoBatch(ctx context.Context, requests []*RPCRequest) ([]RPCResponse, error) {
	raw, err := c.doRaw(ctx, http.MethodPost, "", requests, nil)
	if err != nil {
		return nil, err
	}

	// Try to unmarshal as array first (standard batch response)
	var rpcResponses []RPCResponse
	if err := json.Unmarshal(raw, &rpcResponses); err == nil {
		return rpcResponses, nil
	}

	// If that fails, try to unmarshal as single object (some servers return single object for single request)
	var singleResponse RPCResponse
	if err := json.Unmarshal(raw, &singleResponse); err == nil {
		return []RPCResponse{singleResponse}, nil
	}

	// If both fail, return the original error with more context
	return nil, fmt.Errorf(
		"decode batch RPC: expected array or single object, got: %s",
		string(raw),
	)
}

func (c *BaseClient) NextRequestIDs(n int) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		ids[i] = c.rpcID
		c.rpcID++
	}
	return ids
}

// WaitRateLimit blocks until rate limiter allows request
func (c *BaseClient) WaitRateLimit(ctx context.Context) error {
	if c.rateLimiter == nil {
		return nil
	}
	return c.rateLimiter.Wait(ctx, c.baseURL)
}

func (c *BaseClient) GetNetworkType() string { return c.network }
func (c *BaseClient) GetClientType() string  { return c.clientType }
func (c *BaseClient) GetURL() string         { return c.baseURL }
func (c *BaseClient) Close() error           { return nil }
