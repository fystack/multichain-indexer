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

	"github.com/fystack/indexer/internal/rpc"
)

type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

type JSONRPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	Result  any       `json:"result"`
	Error   *RPCError `json:"error"`
	ID      int       `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type TronClient struct {
	*rpc.HTTPClient
}

func (c *TronClient) Call(ctx context.Context, method string, params []any) (any, error) {
	var lastErr error

	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		node := c.Pool.GetNext()
		if node == "" {
			return nil, fmt.Errorf("no available nodes")
		}

		slog.Debug("Calling RPC", "method", method, "node", node, "attempt", attempt+1)

		// Rate limiting
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

		// Wait before retry
		time.Sleep(c.Config.RetryDelay)

		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("RPC call %q failed after %d attempts: %w", method, c.Config.MaxRetries+1, lastErr)
}

func (c *TronClient) doRequest(ctx context.Context, node, method string, params []any) (any, error) {
	reqBody := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/jsonrpc", node)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error: %s (code: %d)", rpcResp.Error.Message, rpcResp.Error.Code)
	}

	return rpcResp.Result, nil
}
