package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fystack/indexer/internal/rpc"
)

type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int         `json:"id"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
	ID      int             `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type EvmClient struct {
	rpc.HTTPClient
}

func (c *EvmClient) callWithContext(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	var lastErr error

	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.Config.RetryDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		result, err := c.makeRequest(ctx, method, params)
		if err == nil {
			return result, nil
		}

		lastErr = err

		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", c.Config.MaxRetries+1, lastErr)
}

func (c *EvmClient) makeRequest(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	request := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	node := c.Pool.GetNext()

	if err := c.RateLimiter.Wait(ctx, node); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	url := node // EVM chains dùng trực tiếp root URL

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	startTime := time.Now()
	resp, err := c.HTTPClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("request failed to %s (took %v): %w", node, duration, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("HTTP error %d from %s", resp.StatusCode, node)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response JSONRPCResponse
	if err := json.Unmarshal(body, &response); err != nil {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if response.Error != nil {
		return nil, fmt.Errorf("RPC error from %s: %s (code: %d)", node, response.Error.Message, response.Error.Code)
	}

	c.Pool.MarkHealthy(node)
	return response.Result, nil
}
