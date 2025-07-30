package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fystack/indexer/internal/pool"
	"github.com/fystack/indexer/internal/ratelimiter"
)

type Request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	ID int `json:"id"`
}

type ClientConfig struct {
	RequestTimeout time.Duration
	RateLimit      RateLimitConfig
	MaxRetries     int
	RetryDelay     time.Duration
}

type RateLimitConfig struct {
	RequestsPerSecond int
	BurstSize         int
}

type Client struct {
	Pool        *pool.Pool
	RateLimiter *ratelimiter.PooledRateLimiter
	HTTP        *http.Client
	Config      ClientConfig
	FormatURL   func(base string) string // for /jsonrpc or not
}

func NewClient(nodes []string) *Client {
	return NewClientWithConfig(nodes, ClientConfig{
		RequestTimeout: 30 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: 1 * time.Second,
	}, nil)
}

func NewClientWithConfig(nodes []string, config ClientConfig, formatURL func(base string) string) *Client {
	rateDuration := time.Second / time.Duration(config.RateLimit.RequestsPerSecond)
	return &Client{
		Pool:        pool.New(nodes),
		RateLimiter: ratelimiter.NewPooledRateLimiter(rateDuration, config.RateLimit.BurstSize),
		HTTP: &http.Client{
			Timeout: config.RequestTimeout,
		},
		Config:    config,
		FormatURL: formatURL,
	}
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.HTTP.Do(req)
}

func (c *Client) GetRateLimitStats() map[string]ratelimiter.Stats {
	return c.RateLimiter.GetStats()
}

func (c *Client) Close() {
	c.RateLimiter.Close()
}

func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var lastErr error

	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		node := c.Pool.GetNext()
		if node == "" {
			return nil, fmt.Errorf("no available nodes")
		}

		slog.Debug("Calling RPC", "method", method, "node", node, "attempt", attempt+1)

		if err := c.RateLimiter.Wait(ctx, node); err != nil {
			return nil, fmt.Errorf("rate limiter blocked request: %w", err)
		}

		result, err := c.doRequest(ctx, node, method, params)
		if err == nil {
			c.Pool.MarkHealthy(node)
			return result, nil
		}

		c.Pool.MarkFailed(node)
		lastErr = err

		time.Sleep(c.Config.RetryDelay)

		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("RPC call %q failed after %d attempts: %w", method, c.Config.MaxRetries+1, lastErr)
}

func (c *Client) doRequest(ctx context.Context, node, method string, params any) (json.RawMessage, error) {
	reqBody := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := node
	if c.FormatURL != nil {
		url = c.FormatURL(node)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("request failed", "node", node, "method", method, "duration", elapsed, "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var rpcResp Response
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse JSON response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error: %s (code: %d)", rpcResp.Error.Message, rpcResp.Error.Code)
	}

	return rpcResp.Result, nil
}
