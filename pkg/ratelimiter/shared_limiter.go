package ratelimiter

import (
	"fmt"
	"sync"
	"time"
)

// GlobalRateLimiterManager manages shared rate limiters across workers
type GlobalRateLimiterManager struct {
	limiters map[string]*RateLimiter
	mutex    sync.RWMutex
}

var globalRateLimiterManager = &GlobalRateLimiterManager{
	limiters: make(map[string]*RateLimiter),
}

// GetOrCreateRateLimiter returns a shared rate limiter for the given URL and config
func GetOrCreateRateLimiter(url string, rps int, burst int) *RateLimiter {
	globalRateLimiterManager.mutex.Lock()
	defer globalRateLimiterManager.mutex.Unlock()

	key := fmt.Sprintf("%s_%d_%d", url, rps, burst)

	if limiter, exists := globalRateLimiterManager.limiters[key]; exists {
		return limiter
	}

	// Create new rate limiter
	rate := time.Duration(1000/rps) * time.Millisecond // Convert RPS to duration
	limiter := NewRateLimiter(rate, burst)
	globalRateLimiterManager.limiters[key] = limiter

	return limiter
}

// GetOrCreateSharedPooledRateLimiter returns a PooledRateLimiter that uses shared rate limiters
// The scope parameter allows different worker modes to have separate rate limiters
func GetOrCreateSharedPooledRateLimiter(url string, rps int, burst int) *PooledRateLimiter {
	sharedLimiter := GetOrCreateRateLimiter(url, rps, burst)

	// Create a wrapper that always returns the same shared limiter
	return &PooledRateLimiter{
		limiters: map[string]*RateLimiter{
			url: sharedLimiter, // Use URL as key, but always return the same shared limiter
		},
		rate:  time.Duration(1000/rps) * time.Millisecond,
		burst: burst,
	}
}

// GetOrCreateScopedPooledRateLimiter returns a PooledRateLimiter with a scope (e.g., worker mode)
// This allows different worker modes to have separate rate limiters for the same chain
func GetOrCreateScopedPooledRateLimiter(url string, scope string, rps int, burst int) *PooledRateLimiter {
	// Create a scoped key to separate rate limiters by scope
	scopedURL := fmt.Sprintf("%s:%s", url, scope)
	sharedLimiter := GetOrCreateRateLimiter(scopedURL, rps, burst)

	// Create a wrapper that always returns the same shared limiter
	return &PooledRateLimiter{
		limiters: map[string]*RateLimiter{
			url: sharedLimiter, // Use URL as key, but always return the same shared limiter
		},
		rate:  time.Duration(1000/rps) * time.Millisecond,
		burst: burst,
	}
}

// CloseAllRateLimiters closes all global rate limiters
func CloseAllRateLimiters() {
	globalRateLimiterManager.mutex.Lock()
	defer globalRateLimiterManager.mutex.Unlock()

	for _, limiter := range globalRateLimiterManager.limiters {
		limiter.Close()
	}
	globalRateLimiterManager.limiters = make(map[string]*RateLimiter)
}

// GetSharedRateLimiterStats returns statistics about all shared rate limiters
func GetSharedRateLimiterStats() map[string]any {
	globalRateLimiterManager.mutex.RLock()
	defer globalRateLimiterManager.mutex.RUnlock()

	stats := make(map[string]any)
	for key, limiter := range globalRateLimiterManager.limiters {
		available, capacity, rate := limiter.GetStats()
		stats[key] = map[string]any{
			"available_tokens": available,
			"capacity":         capacity,
			"rate_ms":          rate.Milliseconds(),
		}
	}

	return stats
}
