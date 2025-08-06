package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/fystack/transaction-indexer/internal/pool"
	"github.com/fystack/transaction-indexer/internal/ratelimiter"
)

const (
	headerContentType = "Content-Type"
	mimeJSON          = "application/json"
)

// EvmClient represents an EVM-compatible blockchain client
type EvmClient struct {
	Pool        *pool.Pool
	RateLimiter *ratelimiter.PooledRateLimiter
	HTTP        *http.Client
	Config      EvmClientConfig
	FormatURL   func(base string) string // for /jsonrpc or not
}

// EvmClientConfig represents the configuration for EVM client
type EvmClientConfig struct {
	RequestTimeout time.Duration
	RateLimit      RateLimitConfig
	MaxRetries     int
	RetryDelay     time.Duration
}

// RateLimitConfig represents rate limiting configuration
type RateLimitConfig struct {
	RequestsPerSecond int
	BurstSize         int
}

// NewEvmClient creates a new EVM client
func NewEvmClient(nodes []string, config EvmClientConfig, formatURL func(base string) string) *EvmClient {
	rateDuration := time.Second / time.Duration(config.RateLimit.RequestsPerSecond)
	return &EvmClient{
		Pool:        pool.New(nodes),
		RateLimiter: ratelimiter.NewPooledRateLimiter(rateDuration, config.RateLimit.BurstSize),
		HTTP: &http.Client{
			Timeout: config.RequestTimeout,
		},
		Config:    config,
		FormatURL: formatURL,
	}
}

// Call makes a single JSON-RPC Call
func (c *EvmClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
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

// BatchCall makes a batch of JSON-RPC calls
func (c *EvmClient) BatchCall(ctx context.Context, batch []RpcRequest) ([]RpcResponse, error) {
	var lastErr error

	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		node := c.Pool.GetNext()
		if node == "" {
			return nil, fmt.Errorf("no available nodes")
		}

		if err := c.RateLimiter.Wait(ctx, node); err != nil {
			return nil, fmt.Errorf("rate limiter blocked: %w", err)
		}

		responses, err := c.doBatchRequest(ctx, node, batch)
		if err == nil {
			c.Pool.MarkHealthy(node)
			return responses, nil
		}

		c.Pool.MarkFailed(node)
		lastErr = err

		time.Sleep(c.Config.RetryDelay)

		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("batch call failed after %d attempts: %w", c.Config.MaxRetries+1, lastErr)
}

// doRequest performs a single HTTP request
func (c *EvmClient) doRequest(ctx context.Context, node, method string, params any) (json.RawMessage, error) {
	reqBody := RpcRequest{
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
	req.Header.Set(headerContentType, mimeJSON)

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

	var rpcResp RpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse JSON response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error: %s (code: %d)", rpcResp.Error.Message, rpcResp.Error.Code)
	}

	return rpcResp.Result, nil
}

// doBatchRequest performs a batch HTTP request
func (c *EvmClient) doBatchRequest(ctx context.Context, node string, batch []RpcRequest) ([]RpcResponse, error) {
	url := node
	if c.FormatURL != nil {
		url = c.FormatURL(node)
	}

	payload, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("marshal batch: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set(headerContentType, mimeJSON)

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("batch request failed", "node", node, "duration", elapsed, "err", err)
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

	var responses []RpcResponse
	if err := json.Unmarshal(body, &responses); err != nil {
		return nil, fmt.Errorf("parse batch response: %w", err)
	}

	return responses, nil
}

// GetBlock retrieves a block by number
func (c *EvmClient) GetBlock(ctx context.Context, number int64) (json.RawMessage, error) {
	return c.Call(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", number), true})
}

// GetBlocksBatch retrieves multiple blocks in a single batch request
func (c *EvmClient) GetBlocksBatch(ctx context.Context, from, to int64) (map[int64]json.RawMessage, error) {
	if from > to {
		return nil, fmt.Errorf("invalid range: from (%d) > to (%d)", from, to)
	}

	batch := make([]RpcRequest, 0, to-from+1)
	for n := from; n <= to; n++ {
		batch = append(batch, RpcRequest{
			JSONRPC: "2.0",
			Method:  "eth_getBlockByNumber",
			Params:  []any{fmt.Sprintf("0x%x", n), true},
			ID:      int(n - from + 1), // Use sequential IDs starting from 1
		})
	}

	responses, err := c.BatchCall(ctx, batch)
	if err != nil {
		return nil, err
	}

	// Map responses by block number
	result := make(map[int64]json.RawMessage)
	for _, resp := range responses {
		if resp.Error != nil {
			return nil, fmt.Errorf("block request failed: %s", resp.Error.Message)
		}
		blockNumber := from + int64(resp.ID) - 1
		result[blockNumber] = resp.Result
	}

	return result, nil
}

// GetLatestBlockNumber returns the latest block number
func (c *EvmClient) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	result, err := c.Call(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}

	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, fmt.Errorf("decode block number: %w", err)
	}

	num, err := strconv.ParseInt(hexStr, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("parse block number %q: %w", hexStr, err)
	}

	return num, nil
}

// GetTransactionReceiptsBatch attempts to fetch receipts using batch RPC call
func (c *EvmClient) GetTransactionReceiptsBatch(txs []rawTx) ([]*rawReceipt, error) {
	ctx := context.Background()
	batch := make([]RpcRequest, 0, len(txs))

	for idx, tx := range txs {
		batch = append(batch, RpcRequest{
			JSONRPC: "2.0",
			Method:  "eth_getTransactionReceipt",
			Params:  []any{tx.Hash},
			ID:      idx + 1,
		})
	}

	responses, err := c.BatchCall(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("batch receipt call failed: %w", err)
	}

	receipts := make([]*rawReceipt, len(txs))
	for idx, resp := range responses {
		if resp.Error != nil {
			// Skip failed receipts
			continue
		}

		if len(resp.Result) == 0 || string(resp.Result) == "null" {
			continue
		}

		var receipt rawReceipt
		if err := json.Unmarshal(resp.Result, &receipt); err != nil {
			continue
		}
		receipts[idx] = &receipt
	}

	return receipts, nil
}

// Close closes the client and releases resources
func (c *EvmClient) Close() {
	c.RateLimiter.Close()
}
