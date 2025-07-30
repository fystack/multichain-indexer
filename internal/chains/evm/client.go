package evm

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
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	ID int `json:"id"`
}

type EvmClient struct {
	rpc.HTTPClient
}

func (c *EvmClient) callWithContext(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var lastErr error

	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.Config.RetryDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		res, err := c.makeRequest(ctx, method, params)
		if err == nil {
			return res, nil
		}

		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("call to %q failed after %d attempts: %w", method, c.Config.MaxRetries+1, lastErr)
}

func (c *EvmClient) makeRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	node := c.Pool.GetNext()
	if err := c.RateLimiter.Wait(ctx, node); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	reqBody, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, node, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.Client.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("request failed", "node", node, "method", method, "duration", elapsed, "err", err)
		c.Pool.MarkFailed(node)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, node)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		c.Pool.MarkFailed(node)
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	c.Pool.MarkHealthy(node)
	slog.Debug("RPC success", "method", method, "node", node, "duration", elapsed)
	return rpcResp.Result, nil
}
