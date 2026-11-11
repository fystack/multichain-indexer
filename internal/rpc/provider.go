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
	// Backup providers are only used while every non-backup provider is unavailable.
	Backup bool `json:"backup"`

	mu sync.RWMutex // protect all fields below

	// Health metrics
	State               string        `json:"state"`
	LastHealthCheck     time.Time     `json:"last_health_check"`
	AverageResponseTime time.Duration `json:"average_response_time"`
	BlacklistedUntil    time.Time     `json:"blacklisted_until"`
	ConsecutiveErrors   int           `json:"consecutive_errors"`
	// BlacklistStrikes counts blacklists since the last successful request; each strike doubles the cooldown.
	BlacklistStrikes int `json:"blacklist_strikes"`

	blacklistBase time.Duration
	// probeRequired keeps the provider unavailable after its blacklist expires until a probe passes.
	probeRequired bool
	probing       bool
}

// IsAvailable returns true if the provider is not blacklisted or blacklist expired.
func (p *Provider) IsAvailable() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.State != StateBlacklisted {
		return true
	}
	return !p.probeRequired && time.Now().After(p.BlacklistedUntil)
}

// RequiresProbe reports whether the provider must pass a probe before leaving the blacklist.
func (p *Provider) RequiresProbe() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.State == StateBlacklisted && p.probeRequired
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
	p.probeRequired = false
}

// BlacklistEscalating blacklists for base doubled per prior strike, capped at
// max(base, maxD), and returns the applied duration.
func (p *Provider) BlacklistEscalating(base, maxD time.Duration, requireProbe bool) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	d := base
	for i := 0; i < p.BlacklistStrikes && d < maxD; i++ {
		d *= 2
	}
	d = min(d, max(base, maxD))

	p.BlacklistStrikes++
	p.blacklistBase = base
	p.State = StateBlacklisted
	p.BlacklistedUntil = time.Now().Add(d)
	p.probeRequired = requireProbe
	return d
}

// ReblacklistEscalating re-blacklists with the base cooldown of the last escalating blacklist.
func (p *Provider) ReblacklistEscalating(maxD time.Duration) time.Duration {
	p.mu.RLock()
	base := p.blacklistBase
	p.mu.RUnlock()
	return p.BlacklistEscalating(base, maxD, true)
}

// tryStartProbe claims the probe slot for an expired, probe-gated blacklist.
func (p *Provider) tryStartProbe() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probing || !p.probeRequired || p.State != StateBlacklisted || time.Now().Before(p.BlacklistedUntil) {
		return false
	}
	p.probing = true
	return true
}

func (p *Provider) endProbe() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probing = false
}

// Recover reactivates a previously blacklisted provider.
func (p *Provider) Recover() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.State = StateDegraded
	p.BlacklistedUntil = time.Time{}
	p.ConsecutiveErrors = 0
	p.probeRequired = false
}

// Success resets errors and updates health metrics.
func (p *Provider) Success(elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ConsecutiveErrors = 0
	p.BlacklistStrikes = 0
	p.State = StateHealthy
	if p.AverageResponseTime == 0 {
		p.AverageResponseTime = elapsed
	} else {
		p.AverageResponseTime = (p.AverageResponseTime + elapsed) / 2
	}
	p.LastHealthCheck = time.Now()
}
