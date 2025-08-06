package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEvmClient(t *testing.T) {
	nodes := []string{"http://localhost:8545", "http://localhost:8546"}
	config := EvmClientConfig{
		RequestTimeout: 30 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: 1 * time.Second,
	}

	client := NewEvmClient(nodes, config, nil)

	assert.NotNil(t, client)
	assert.NotNil(t, client.Pool)
	assert.NotNil(t, client.RateLimiter)
	assert.NotNil(t, client.HTTP)
	assert.Equal(t, config, client.Config)
	// Check that pool was created with the correct nodes
	total, healthy, failed := client.Pool.GetStats()
	assert.Equal(t, len(nodes), total)
	assert.Equal(t, len(nodes), healthy)
	assert.Equal(t, 0, failed)
}

func TestEvmClientCall(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse request body
		var reqBody RpcRequest
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)

		// Return mock response
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      reqBody.ID,
			Result:  json.RawMessage(`"0x1234"`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test successful call
	result, err := client.Call(context.Background(), "eth_blockNumber", []any{})
	require.NoError(t, err)
	assert.Equal(t, `"0x1234"`, string(result))
}

func TestEvmClientCallWithError(t *testing.T) {
	// Create a test server that returns an error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      1,
			Error: &struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}{
				Code:    -32601,
				Message: "Method not found",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test call with error
	result, err := client.Call(context.Background(), "eth_invalidMethod", []any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Method not found")
	assert.Nil(t, result)
}

func TestEvmClientGetLatestBlockNumber(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody RpcRequest
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)

		// Return mock block number
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      reqBody.ID,
			Result:  json.RawMessage(`"0x123456"`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test GetLatestBlockNumber
	blockNumber, err := client.GetLatestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0x123456), blockNumber)
}

func TestEvmClientGetBlock(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody RpcRequest
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)

		// Return mock block
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      reqBody.ID,
			Result: json.RawMessage(`{
				"number": "0x1234",
				"hash": "0xabcd",
				"transactions": []
			}`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test GetBlock
	block, err := client.GetBlock(context.Background(), 0x1234)
	require.NoError(t, err)
	assert.NotNil(t, block)
	assert.Contains(t, string(block), `"number":"0x1234"`)
}

func TestEvmClientGetBlocksBatch(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requests []RpcRequest
		err := json.NewDecoder(r.Body).Decode(&requests)
		require.NoError(t, err)

		// Return mock batch response
		var responses []RpcResponse
		for _, req := range requests {
			response := RpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"number": "0x` + fmt.Sprintf("%x", req.ID) + `",
					"hash": "0xabcd",
					"transactions": []
				}`),
			}
			responses = append(responses, response)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responses)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test GetBlocksBatch
	blockMap, err := client.GetBlocksBatch(context.Background(), 1, 3)
	require.NoError(t, err)
	assert.Len(t, blockMap, 3)

	// Check each response
	for blockNum := int64(1); blockNum <= 3; blockNum++ {
		blockData, exists := blockMap[blockNum]
		assert.True(t, exists)
		assert.NotNil(t, blockData)
		assert.Contains(t, string(blockData), fmt.Sprintf(`"number":"0x%x"`, blockNum))
	}
}

func TestEvmClientBatchCall(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requests []RpcRequest
		err := json.NewDecoder(r.Body).Decode(&requests)
		require.NoError(t, err)

		// Return mock batch response
		var responses []RpcResponse
		for _, req := range requests {
			response := RpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`"0x` + fmt.Sprintf("%x", req.ID) + `"`),
			}
			responses = append(responses, response)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responses)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Create batch request
	batch := []RpcRequest{
		{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: 1},
		{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: 2},
		{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: 3},
	}

	// Test BatchCall
	responses, err := client.BatchCall(context.Background(), batch)
	require.NoError(t, err)
	assert.Len(t, responses, 3)

	// Check each response
	for i, resp := range responses {
		assert.Equal(t, i+1, resp.ID)
		assert.NotNil(t, resp.Result)
	}
}

func TestEvmClientRetryLogic(t *testing.T) {
	attemptCount := 0
	// Create a test server that fails first, then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount == 1 {
			// First attempt fails
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Second attempt succeeds
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`"0x1234"`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client with retry config
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 2,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test retry logic
	result, err := client.Call(context.Background(), "eth_blockNumber", []any{})
	require.NoError(t, err)
	assert.Equal(t, `"0x1234"`, string(result))
	assert.Equal(t, 2, attemptCount)
}

func TestEvmClientContextCancellation(t *testing.T) {
	// Create a test server that delays
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`"0x1234"`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, nil)

	// Test context cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := client.Call(ctx, "eth_blockNumber", []any{})
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestEvmClientClose(t *testing.T) {
	client := NewEvmClient([]string{"http://localhost:8545"}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: 1 * time.Second,
	}, nil)

	// Test that Close doesn't panic
	assert.NotPanics(t, func() {
		client.Close()
	})
}

func TestEvmClientWithURLFormatting(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := RpcResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`"0x1234"`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create URL formatter function
	formatURL := func(base string) string {
		return base + "/jsonrpc"
	}

	// Create client with URL formatting
	client := NewEvmClient([]string{server.URL}, EvmClientConfig{
		RequestTimeout: 5 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         100,
		},
		MaxRetries: 1,
		RetryDelay: 100 * time.Millisecond,
	}, formatURL)

	assert.NotNil(t, client.FormatURL)
	assert.Equal(t, server.URL+"/jsonrpc", client.FormatURL(server.URL))
}
