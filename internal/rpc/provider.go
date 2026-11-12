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
	// blacklistGen changes on every blacklist or recovery so a stale probe result can be dropped.
	blacklistGen       uint64
	emergencyRecovered bool
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
	p.blacklistGen++
}

// BlacklistEscalating blacklists for base doubled per prior strike, capped at max(base, maxD).
func (p *Provider) BlacklistEscalating(base, maxD time.Duration, requireProbe bool) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.blacklistEscalatingLocked(base, maxD, requireProbe)
}

func (p *Provider) blacklistEscalatingLocked(base, maxD time.Duration, requireProbe bool) time.Duration {
	now := time.Now()

	// Failures during an active blacklist are the same incident: no new strike.
	if p.State == StateBlacklisted && now.Before(p.BlacklistedUntil) {
		if until := now.Add(base); until.After(p.BlacklistedUntil) {
			p.BlacklistedUntil = until
		}
		return p.BlacklistedUntil.Sub(now)
	}

	// Failing right after emergency recovery is the same outage, so the strike is reused.
	if !p.emergencyRecovered || p.BlacklistStrikes == 0 {
		p.BlacklistStrikes++
	}
	p.emergencyRecovered = false

	d := base
	for i := 1; i < p.BlacklistStrikes && d < maxD; i++ {
		d *= 2
	}
	d = min(d, max(base, maxD))

	p.blacklistBase = base
	p.State = StateBlacklisted
	p.BlacklistedUntil = now.Add(d)
	p.probeRequired = requireProbe
	p.blacklistGen++
	return d
}

type expiredBlacklistAction int

const (
	expiredBlacklistUnchanged expiredBlacklistAction = iota
	expiredBlacklistRecovered
	expiredBlacklistNeedsProbe
)

// settleExpiredBlacklist recovers an expired blacklist, or claims the probe slot when it is probe-gated.
func (p *Provider) settleExpiredBlacklist(canProbe bool) (expiredBlacklistAction, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.State != StateBlacklisted || time.Now().Before(p.BlacklistedUntil) {
		return expiredBlacklistUnchanged, 0
	}
	if canProbe && p.probeRequired {
		if p.probing {
			return expiredBlacklistUnchanged, 0
		}
		p.probing = true
		return expiredBlacklistNeedsProbe, p.blacklistGen
	}
	p.recoverLocked()
	return expiredBlacklistRecovered, 0
}

// finishProbe applies a probe result unless the provider was blacklisted or recovered while it ran.
func (p *Provider) finishProbe(gen uint64, passed bool, maxD time.Duration) (applied bool, cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.probing = false
	if p.blacklistGen != gen {
		return false, 0
	}
	if passed {
		p.recoverLocked()
		return true, 0
	}
	return true, p.blacklistEscalatingLocked(p.blacklistBase, maxD, true)
}

// Recover reactivates a previously blacklisted provider.
func (p *Provider) Recover() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recoverLocked()
}

func (p *Provider) emergencyRecover() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recoverLocked()
	p.emergencyRecovered = true
}

func (p *Provider) recoverLocked() {
	p.State = StateDegraded
	p.BlacklistedUntil = time.Time{}
	p.ConsecutiveErrors = 0
	p.probeRequired = false
	p.blacklistGen++
}

// Success resets errors and updates health metrics.
func (p *Provider) Success(elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ConsecutiveErrors = 0
	p.emergencyRecovered = false
	p.BlacklistStrikes = 0
	p.State = StateHealthy
	if p.AverageResponseTime == 0 {
		p.AverageResponseTime = elapsed
	} else {
		p.AverageResponseTime = (p.AverageResponseTime + elapsed) / 2
	}
	p.LastHealthCheck = time.Now()
}
