package ratelimiter

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter wraps golang.org/x/time/rate.Limiter for better performance
type RateLimiter struct {
	limiter *rate.Limiter
	burst   int
	rps     int
}

// NewRateLimiter creates a new rate limiter using golang.org/x/time/rate
// ratePerToken: time between token generation (e.g., 100ms for 10 RPS)
// burst: maximum number of tokens in bucket
func NewRateLimiter(ratePerToken time.Duration, burst int) *RateLimiter {
	// Convert rate duration to RPS
	rps := int(time.Second / ratePerToken)
	if rps <= 0 {
		rps = 1
	}

	return &RateLimiter{
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
		burst:   burst,
		rps:     rps,
	}
}

// NewRateLimiterFromRPS creates a rate limiter directly from RPS
func NewRateLimiterFromRPS(rps int, burst int) *RateLimiter {
	if rps <= 0 {
		rps = 1
	}
	return &RateLimiter{
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
		burst:   burst,
		rps:     rps,
	}
}

// Wait blocks until a token is available
// This is efficient and won't cause contention issues
func (rl *RateLimiter) Wait(ctx context.Context) error {
	return rl.limiter.Wait(ctx)
}

// TryAcquire attempts to acquire a token without blocking
func (rl *RateLimiter) TryAcquire() bool {
	return rl.limiter.Allow()
}

// Close is a no-op for compatibility (golang.org/x/time/rate doesn't need cleanup)
func (rl *RateLimiter) Close() {
	// No cleanup needed for golang.org/x/time/rate
}

// GetStats returns current limiter statistics
func (rl *RateLimiter) GetStats() (available, capacity int, rateDuration time.Duration) {
	// Estimate available tokens (this is approximate)
	available = int(rl.limiter.Tokens())
	if available < 0 {
		available = 0
	}
	capacity = rl.burst
	rateDuration = time.Second / time.Duration(rl.rps)
	return
}
