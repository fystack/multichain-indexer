package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/core"
	"github.com/fystack/transaction-indexer/pkg/ratelimiter"
)

// AuthConfig holds authentication configuration
type AuthConfig struct {
	Type     string            `json:"type"`     // "bearer", "api_key", "basic", "custom"
	Token    string            `json:"token"`    // For bearer/api_key
	Username string            `json:"username"` // For basic auth
	Password string            `json:"password"` // For basic auth
	Headers  map[string]string `json:"headers"`  // Custom headers
}

// NodeToAuthConfig converts a core.Node to rpc.AuthConfig
// This should be called after the config has been loaded and processed by core.Load()
func NodeToAuthConfig(node core.Node) *AuthConfig {
	auth := &AuthConfig{}

	// Priority 1: If headers are present, use custom auth
	if len(node.Headers) > 0 {
		auth.Type = "custom"
		auth.Headers = make(map[string]string)

		// Copy all headers (API key substitution already done by finalizeNodes)
		maps.Copy(auth.Headers, node.Headers)
		return auth
	}

	// Priority 2: If ApiKey is present, determine auth type from common patterns
	if node.ApiKey != "" {
		// Check if it looks like a bearer token or API key
		if strings.HasPrefix(strings.ToLower(node.ApiKey), "bearer ") {
			auth.Type = "bearer"
			auth.Token = strings.TrimPrefix(node.ApiKey, "bearer ")
			auth.Token = strings.TrimPrefix(auth.Token, "Bearer ")
		} else {
			// Default to bearer token for most blockchain APIs
			auth.Type = "bearer"
			auth.Token = node.ApiKey
		}

		return auth
	}

	// No authentication needed
	return nil
}

type NetworkClient interface {
	CallRPC(ctx context.Context, method string, params any) (*RPCResponse, error)
	Do(ctx context.Context, method, endpoint string, body any, params map[string]string) ([]byte, error)
	IsHealthy(ctx context.Context) bool
	GetNetworkType() string
	GetClientType() string
	GetURL() string
	Close() error
}

type genericClient struct {
	httpClient  *http.Client
	baseURL     string
	auth        *AuthConfig
	network     string
	clientType  string
	rateLimiter *ratelimiter.PooledRateLimiter

	rpcID int64
	mutex sync.Mutex
}

func NewGenericClient(baseURL, network, clientType string, auth *AuthConfig, timeout time.Duration, rateLimiter *ratelimiter.PooledRateLimiter) *genericClient {
	return &genericClient{
		httpClient:  &http.Client{Timeout: timeout},
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		auth:        auth,
		network:     network,
		clientType:  clientType,
		rateLimiter: rateLimiter,
		rpcID:       1,
	}
}

func (c *genericClient) CallRPC(ctx context.Context, method string, params any) (*RPCResponse, error) {
	if c.clientType != ClientTypeRPC {
		return nil, fmt.Errorf("client is %s, not RPC", c.clientType)
	}
	c.mutex.Lock()
	reqID := c.rpcID
	c.rpcID++
	c.mutex.Unlock()

	// Rate limit
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, c.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	req := &RPCRequest{ID: reqID, JSONRPC: "2.0", Method: method, Params: params}
	raw, err := c.Do(ctx, http.MethodPost, "", req, nil)
	if err != nil {
		return nil, err
	}

	var rpcResp RPCResponse
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal RPC response: %w", err)
	}
	if rpcResp.Error != nil {
		return &rpcResp, rpcResp.Error
	}
	return &rpcResp, nil
}

func (c *genericClient) Do(ctx context.Context, method, endpoint string, body any, params map[string]string) ([]byte, error) {
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, c.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	url := c.baseURL + endpoint
	if len(params) > 0 {
		q := make([]string, 0, len(params))
		for k, v := range params {
			q = append(q, fmt.Sprintf("%s=%s", k, v))
		}
		url += "?" + strings.Join(q, "&")
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuthHeaders(req)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	slog.Debug("HTTP request completed", "url", url, "elapsed", time.Since(start))

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, string(data))
	}
	return data, nil
}

func (c *genericClient) IsHealthy(ctx context.Context) bool {
	key := c.network + c.clientType

	switch key {
	case NetworkEVM + ClientTypeRPC:
		resp, err := c.CallRPC(ctx, "net_version", nil)
		return err == nil && resp.Error == nil

	case NetworkSolana + ClientTypeRPC:
		resp, err := c.CallRPC(ctx, "getHealth", nil)
		return err == nil && resp.Error == nil

	case NetworkBitcoin + ClientTypeRPC:
		resp, err := c.CallRPC(ctx, "getblockchaininfo", nil)
		return err == nil && resp.Error == nil

	case NetworkTron + ClientTypeREST:
		_, err := c.Do(ctx, http.MethodPost, "/wallet/getnowblock", nil, nil)
		return err == nil

	default:
		// fallback REST GET /health
		_, err := c.Do(ctx, http.MethodGet, "/health", nil, nil)
		return err == nil
	}
}

func (c *genericClient) setAuthHeaders(req *http.Request) {
	if c.auth == nil {
		return
	}
	switch c.auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+c.auth.Token)
	case "api_key":
		req.Header.Set("X-API-Key", c.auth.Token)
	case "basic":
		req.SetBasicAuth(c.auth.Username, c.auth.Password)
	case "custom":
		for k, v := range c.auth.Headers {
			req.Header.Set(k, v)
		}
	}
}

func (c *genericClient) GetNetworkType() string { return c.network }
func (c *genericClient) GetClientType() string  { return c.clientType }
func (c *genericClient) GetURL() string         { return c.baseURL }
func (c *genericClient) Close() error           { return nil }
