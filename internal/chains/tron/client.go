package tron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fystack/transaction-indexer/internal/pool"
	"github.com/fystack/transaction-indexer/internal/ratelimiter"
)

const (
	headerContentType = "Content-Type"
	headerAccept      = "Accept"
	mimeJSON          = "application/json"
)

type TronClient struct {
	Pool        *pool.Pool
	RateLimiter *ratelimiter.PooledRateLimiter
	HTTP        *http.Client
	Config      TronClientConfig
}

type TronClientConfig struct {
	RequestTimeout time.Duration
	RateLimit      RateLimitConfig
	MaxRetries     int
	RetryDelay     time.Duration
}

type RateLimitConfig struct {
	RequestsPerSecond int
	BurstSize         int
}

func NewTronClient(nodes []string, config TronClientConfig) *TronClient {
	rateDuration := time.Second / time.Duration(config.RateLimit.RequestsPerSecond)
	return &TronClient{
		Pool:        pool.New(nodes),
		RateLimiter: ratelimiter.NewPooledRateLimiter(rateDuration, config.RateLimit.BurstSize),
		HTTP: &http.Client{
			Timeout: config.RequestTimeout,
		},
		Config: config,
	}
}

func (c *TronClient) Call(ctx context.Context, endpoint string, params any) (json.RawMessage, error) {
	var lastErr error

	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		node := c.Pool.GetNext()
		if node == "" {
			return nil, fmt.Errorf("no available nodes")
		}

		slog.Debug("Calling Tron API", "endpoint", endpoint, "node", node, "attempt", attempt+1)

		if err := c.RateLimiter.Wait(ctx, node); err != nil {
			return nil, fmt.Errorf("rate limiter blocked request: %w", err)
		}

		result, err := c.doRequest(ctx, node, endpoint, params)
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

	return nil, fmt.Errorf("tron API call %q failed after %d attempts: %w", endpoint, c.Config.MaxRetries+1, lastErr)
}

func (c *TronClient) doRequest(ctx context.Context, node, endpoint string, params any) (json.RawMessage, error) {
	// For Tron API, we send the params directly as the request body
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Construct the full URL with endpoint
	url := fmt.Sprintf("%s/%s", node, endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set(headerContentType, mimeJSON)
	req.Header.Set(headerAccept, mimeJSON)

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("request failed", "node", node, "endpoint", endpoint, "duration", elapsed, "err", err)
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

	// Tron API responses are direct JSON, not wrapped in JSON-RPC format
	return body, nil
}

// Convenience methods for common Tron API calls
func (c *TronClient) GetBlock(ctx context.Context, idOrNum string, detail bool) (json.RawMessage, error) {
	params := map[string]any{
		"id_or_num": idOrNum,
		"detail":    detail,
	}
	return c.Call(ctx, "wallet/getblock", params)
}

func (c *TronClient) GetTransactionInfo(ctx context.Context, txID string) (json.RawMessage, error) {
	params := map[string]any{
		"value": txID,
	}
	return c.Call(ctx, "wallet/gettransactioninfobyid", params)
}

func (c *TronClient) GetTransaction(ctx context.Context, txID string) (json.RawMessage, error) {
	params := map[string]any{
		"value": txID,
	}
	return c.Call(ctx, "wallet/gettransactionbyid", params)
}

func (c *TronClient) GetAccount(ctx context.Context, address string) (json.RawMessage, error) {
	params := map[string]any{
		"address": address,
	}
	return c.Call(ctx, "wallet/getaccount", params)
}

func (c *TronClient) GetNowBlock(ctx context.Context) (json.RawMessage, error) {
	params := map[string]any{}
	return c.Call(ctx, "wallet/getnowblock", params)
}

func (c *TronClient) GetBlockByNum(ctx context.Context, num int64) (json.RawMessage, error) {
	params := map[string]any{
		"num": num,
	}
	return c.Call(ctx, "wallet/getblockbynum", params)
}

func (c *TronClient) Close() {
	c.RateLimiter.Close()
}
