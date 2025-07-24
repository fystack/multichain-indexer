package ratelimiter

import (
	"context"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter
type RateLimiter struct {
	tokens chan struct{}
	ticker *time.Ticker
	rate   time.Duration
	burst  int
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRateLimiter creates a new rate limiter
// rate: time between token generation (e.g., 100ms for 10 RPS)
// burst: maximum number of tokens in bucket
func NewRateLimiter(rate time.Duration, burst int) *RateLimiter {
	ctx, cancel := context.WithCancel(context.Background())

	rl := &RateLimiter{
		tokens: make(chan struct{}, burst),
		rate:   rate,
		burst:  burst,
		ctx:    ctx,
		cancel: cancel,
	}

	// Fill initial tokens
	for i := 0; i < burst; i++ {
		rl.tokens <- struct{}{}
	}

	// Start token generation
	rl.start()
	return rl
}

// Wait blocks until a token is available
func (rl *RateLimiter) Wait(ctx context.Context) error {
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-rl.ctx.Done():
		return rl.ctx.Err()
	}
}

// TryAcquire attempts to acquire a token without blocking
func (rl *RateLimiter) TryAcquire() bool {
	select {
	case <-rl.tokens:
		return true
	default:
		return false
	}
}

// start begins token generation
func (rl *RateLimiter) start() {
	rl.ticker = time.NewTicker(rl.rate)
	rl.wg.Add(1)

	go func() {
		defer rl.wg.Done()
		defer rl.ticker.Stop()

		for {
			select {
			case <-rl.ticker.C:
				select {
				case rl.tokens <- struct{}{}:
				default:
					// Bucket is full, drop token
				}
			case <-rl.ctx.Done():
				return
			}
		}
	}()
}

// Close stops the rate limiter
func (rl *RateLimiter) Close() {
	rl.cancel()
	rl.wg.Wait()
}

// GetStats returns current limiter statistics
func (rl *RateLimiter) GetStats() (available, capacity int, rate time.Duration) {
	return len(rl.tokens), rl.burst, rl.rate
}
