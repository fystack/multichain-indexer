package rpc

import (
	"sync"
	"time"
)

// Provider represents a blockchain network provider
type Provider struct {
	Name       string        `json:"name"`
	URL        string        `json:"url"`
	Network    string        `json:"network"`
	ClientType string        `json:"client_type"`
	Client     NetworkClient `json:"-"`

	mu sync.RWMutex // protect all fields below

	// Health metrics
	State               string        `json:"state"`
	LastHealthCheck     time.Time     `json:"last_health_check"`
	AverageResponseTime time.Duration `json:"average_response_time"`
	BlacklistedUntil    time.Time     `json:"blacklisted_until"`
	ConsecutiveErrors   int           `json:"consecutive_errors"`
}

// IsAvailable returns true if the provider is not blacklisted or blacklist expired.
func (p *Provider) IsAvailable() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.State != StateBlacklisted || time.Now().After(p.BlacklistedUntil)
}

// IsExpiredBlacklist checks if provider's blacklist duration has expired.
func (p *Provider) IsExpiredBlacklist() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.State == StateBlacklisted && time.Now().After(p.BlacklistedUntil)
}

// Fail increases error count and updates state based on threshold.
func (p *Provider) Fail(cfg *FailoverConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ConsecutiveErrors++
	switch {
	case p.ConsecutiveErrors >= cfg.ErrorThreshold:
		p.State = StateUnhealthy
	case p.ConsecutiveErrors >= 2:
		p.State = StateDegraded
	}
}

// Blacklist marks provider as temporarily unavailable.
func (p *Provider) Blacklist(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.State = StateBlacklisted
	p.BlacklistedUntil = time.Now().Add(d)
}

// Recover reactivates a previously blacklisted provider.
func (p *Provider) Recover() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.State = StateDegraded
	p.BlacklistedUntil = time.Time{}
	p.ConsecutiveErrors = 0
}

// Success resets errors and updates health metrics.
func (p *Provider) Success(elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ConsecutiveErrors = 0
	p.State = StateHealthy
	if p.AverageResponseTime == 0 {
		p.AverageResponseTime = elapsed
	} else {
		p.AverageResponseTime = (p.AverageResponseTime + elapsed) / 2
	}
	p.LastHealthCheck = time.Now()
}
