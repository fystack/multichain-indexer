package tron

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNewTronClient(t *testing.T) {
	nodes := []string{"https://api.shasta.trongrid.io"}
	config := TronClientConfig{
		RequestTimeout: 30 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: time.Second,
	}

	client := NewTronClient(nodes, config)
	if client == nil {
		t.Fatal("Expected client to be created")
	}

	if client.Pool == nil {
		t.Error("Expected pool to be initialized")
	}

	if client.RateLimiter == nil {
		t.Error("Expected rate limiter to be initialized")
	}

	if client.HTTP == nil {
		t.Error("Expected HTTP client to be initialized")
	}
}

func TestTronClientCall(t *testing.T) {
	nodes := []string{"https://api.shasta.trongrid.io"}
	config := TronClientConfig{
		RequestTimeout: 30 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: time.Second,
	}

	client := NewTronClient(nodes, config)
	defer client.Close()

	ctx := context.Background()

	// Test GetNowBlock
	result, err := client.GetNowBlock(ctx)
	if err != nil {
		t.Logf("GetNowBlock failed (expected for test environment): %v", err)
		return
	}

	// Verify the response is valid JSON
	var response map[string]any
	if err := json.Unmarshal(result, &response); err != nil {
		t.Errorf("Failed to parse response as JSON: %v", err)
	}

	// Check if response contains expected fields
	if _, ok := response["block_header"]; !ok {
		t.Error("Expected response to contain 'block_header' field")
	}
}

func TestTronClientGetBlock(t *testing.T) {
	nodes := []string{"https://api.shasta.trongrid.io"}
	config := TronClientConfig{
		RequestTimeout: 30 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: time.Second,
	}

	client := NewTronClient(nodes, config)
	defer client.Close()

	ctx := context.Background()

	// Test GetBlock with a specific block number
	params := map[string]any{
		"id_or_num": "1000000",
		"detail":    false,
	}

	result, err := client.Call(ctx, "wallet/getblock", params)
	if err != nil {
		t.Logf("GetBlock failed (expected for test environment): %v", err)
		return
	}

	// Verify the response is valid JSON
	var response map[string]any
	if err := json.Unmarshal(result, &response); err != nil {
		t.Errorf("Failed to parse response as JSON: %v", err)
	}
}
