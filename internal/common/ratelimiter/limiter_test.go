package ratelimiter

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter_Basic(t *testing.T) {
	// Create rate limiter: 1 token per 100ms, max 5 tokens in bucket
	rl := NewRateLimiter(100*time.Millisecond, 5)
	defer rl.Close()

	ctx := context.Background()

	// Use all 5 tokens immediately
	for i := 0; i < 5; i++ {
		err := rl.Wait(ctx)
		if err != nil {
			t.Fatalf("Failed to get token %d: %v", i+1, err)
		}
	}

	// Now all tokens are used, so the next call must wait at least 100ms for a new token
	start := time.Now()
	err := rl.Wait(ctx)
	if err != nil {
		t.Fatalf("Failed to get token after waiting: %v", err)
	}
	elapsed := time.Since(start)

	// Check if we actually waited
	if elapsed < 80*time.Millisecond {
		t.Errorf("Expected to wait at least 80ms, but waited %v", elapsed)
	}
}

func TestRateLimiter_TryAcquire(t *testing.T) {
	rl := NewRateLimiter(100*time.Millisecond, 2)
	defer rl.Close()

	// Should be able to acquire 2 tokens immediately
	if !rl.TryAcquire() {
		t.Error("Failed to acquire first token")
	}
	if !rl.TryAcquire() {
		t.Error("Failed to acquire second token")
	}
	available, capacity, rate := rl.GetStats()
	t.Logf("Available: %d, Capacity: %d, Rate: %v\n", available, capacity, rate)

	// 3rd attempt should fail
	if rl.TryAcquire() {
		t.Error("Should not have acquired 3rd token")
	}
}

func TestPooledRateLimiter(t *testing.T) {
	prl := NewPooledRateLimiter(100*time.Millisecond, 2)
	defer prl.Close()

	ctx := context.Background()

	// Should be able to acquire from different nodes independently
	if err := prl.Wait(ctx, "node1"); err != nil {
		t.Fatalf("Failed to acquire from node1: %v", err)
	}
	if err := prl.Wait(ctx, "node2"); err != nil {
		t.Fatalf("Failed to acquire from node2: %v", err)
	}

	// Each node should have its own limits
	if !prl.TryAcquire("node1") {
		t.Error("Should be able to acquire another token from node1")
	}
	if !prl.TryAcquire("node2") {
		t.Error("Should be able to acquire another token from node2")
	}

	// Both nodes should now be at limit
	if prl.TryAcquire("node1") {
		t.Error("Node1 should be at limit")
	}
	if prl.TryAcquire("node2") {
		t.Error("Node2 should be at limit")
	}
}
