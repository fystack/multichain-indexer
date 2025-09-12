package rpc

import "time"

// Provider represents a blockchain network provider
type Provider struct {
	Name       string        `json:"name"`
	URL        string        `json:"url"`
	Network    string        `json:"network"`
	ClientType string        `json:"client_type"`
	Client     NetworkClient `json:"-"`

	// Health metrics
	State               string        `json:"state"`
	LastHealthCheck     time.Time     `json:"last_health_check"`
	AverageResponseTime time.Duration `json:"average_response_time"`
	BlacklistedUntil    time.Time     `json:"blacklisted_until"`
	ConsecutiveErrors   int           `json:"consecutive_errors"`
}

func (p *Provider) IsAvailable() bool {
	return p.State != StateBlacklisted || time.Now().After(p.BlacklistedUntil)
}

func (p *Provider) IsExpiredBlacklist() bool {
	return p.State == StateBlacklisted && time.Now().After(p.BlacklistedUntil)
}

func (p *Provider) Fail(cfg *FailoverConfig) {
	p.ConsecutiveErrors++
	switch {
	case p.ConsecutiveErrors >= cfg.ErrorThreshold:
		p.State = StateUnhealthy
	case p.ConsecutiveErrors >= 2:
		p.State = StateDegraded
	}
}

func (p *Provider) Blacklist(d time.Duration) {
	p.State = StateBlacklisted
	p.BlacklistedUntil = time.Now().Add(d)
}

func (p *Provider) Recover() {
	p.State = StateDegraded
	p.BlacklistedUntil = time.Time{}
	p.ConsecutiveErrors = 0
}

func (p *Provider) Success(elapsed time.Duration) {
	p.ConsecutiveErrors = 0
	p.State = StateHealthy
	p.AverageResponseTime = (p.AverageResponseTime + elapsed) / 2
	p.LastHealthCheck = time.Now()
}
