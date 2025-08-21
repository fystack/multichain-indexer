package ratelimiter

import (
	"context"
	"sync"
	"time"
)

// Stats represents the current state of a rate limiter
type Stats struct {
	AvailableTokens int
	Capacity        int
	Rate            time.Duration
}

// PooledRateLimiter manages rate limiters per node
type PooledRateLimiter struct {
	limiters map[string]*RateLimiter
	mutex    sync.RWMutex
	rate     time.Duration
	burst    int
}

// NewPooledRateLimiter creates a new pooled rate limiter
func NewPooledRateLimiter(rate time.Duration, burst int) *PooledRateLimiter {
	return &PooledRateLimiter{
		limiters: make(map[string]*RateLimiter),
		rate:     rate,
		burst:    burst,
	}
}

// Wait waits for permission to make a request to the specified node
func (p *PooledRateLimiter) Wait(ctx context.Context, node string) error {
	limiter := p.getLimiter(node)
	return limiter.Wait(ctx)
}

// TryAcquire attempts to acquire permission without blocking
func (p *PooledRateLimiter) TryAcquire(node string) bool {
	limiter := p.getLimiter(node)
	return limiter.TryAcquire()
}

// getLimiter gets or creates a rate limiter for the specified node
func (p *PooledRateLimiter) getLimiter(node string) *RateLimiter {
	p.mutex.RLock()
	limiter, exists := p.limiters[node]
	p.mutex.RUnlock()

	if exists {
		return limiter
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Double-check in case another goroutine created it
	if limiter, exists := p.limiters[node]; exists {
		return limiter
	}

	limiter = NewRateLimiter(p.rate, p.burst)
	p.limiters[node] = limiter
	return limiter
}

// Close closes all rate limiters
func (p *PooledRateLimiter) Close() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, limiter := range p.limiters {
		limiter.Close()
	}
	p.limiters = make(map[string]*RateLimiter)
}

// GetStats returns statistics for all nodes
func (p *PooledRateLimiter) GetStats() map[string]Stats {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	stats := make(map[string]Stats)
	for node, limiter := range p.limiters {
		available, capacity, rate := limiter.GetStats()
		stats[node] = Stats{
			AvailableTokens: available,
			Capacity:        capacity,
			Rate:            rate,
		}
	}
	return stats
}
