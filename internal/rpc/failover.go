package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/retry"
)

// FailoverConfig config for failover
type FailoverConfig struct {
	HealthCheckInterval time.Duration
	EnableBlacklisting  bool
	MinActiveProviders  int
	ErrorThreshold      int
	DefaultTimeout      time.Duration
}

func DefaultFailoverConfig() FailoverConfig {
	return FailoverConfig{
		HealthCheckInterval: 30 * time.Second,
		EnableBlacklisting:  true,
		MinActiveProviders:  2,
		ErrorThreshold:      5,
		DefaultTimeout:      10 * time.Second,
	}
}

// Failover manages multiple providers for a specific client type T
type Failover[T NetworkClient] struct {
	providers       []*Provider
	currentIndex    int
	config          FailoverConfig
	lastHealthCheck time.Time
}

// NewFailover creates a new type-safe Failover[T]
func NewFailover[T NetworkClient](config *FailoverConfig) *Failover[T] {
	if config == nil {
		c := DefaultFailoverConfig()
		config = &c
	}
	return &Failover[T]{
		providers:    make([]*Provider, 0),
		currentIndex: -1,
		config:       *config,
	}
}

// AddProvider adds a provider, ensuring its Client is of type T
func (f *Failover[T]) AddProvider(p *Provider) error {
	if _, ok := p.Client.(T); !ok {
		return fmt.Errorf("invalid provider client type: expected %T, got %T", *new(T), p.Client)
	}
	f.providers = append(f.providers, p)
	if f.currentIndex == -1 {
		f.currentIndex = 0
	}
	logger.Info("Added provider", "name", p.Name, "url", p.URL)
	return nil
}

// GetBestProvider returns the current best provider
func (f *Failover[T]) GetBestProvider() (*Provider, error) {
	if len(f.providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}
	for _, p := range f.providers {
		if p.IsExpiredBlacklist() {
			logger.Info("Recovering expired blacklisted provider", "provider", p.Name)
			p.Recover()
		}
	}
	if f.currentIndex >= 0 && f.currentIndex < len(f.providers) {
		cur := f.providers[f.currentIndex]
		if cur.IsAvailable() {
			return cur, nil
		} else {
			logger.Warn("Current provider not available, finding alternative",
				"provider", cur.Name,
				"state", cur.State,
				"consecutive_errors", cur.ConsecutiveErrors)
		}
	}
	return f.findNextAvailableProvider()
}

// GetAvailableProviders returns all currently available providers
func (f *Failover[T]) GetAvailableProviders() []*Provider {
	var available []*Provider
	for _, p := range f.providers {
		if p.IsExpiredBlacklist() {
			p.Recover()
		}
		if p.IsAvailable() {
			available = append(available, p)
		}
	}
	return available
}

func (f *Failover[T]) findNextAvailableProvider() (*Provider, error) {
	start := f.currentIndex
	logger.Info(
		"Searching for available provider",
		"start_index",
		start,
		"total_providers",
		len(f.providers),
	)

	for i := 0; i < len(f.providers); i++ {
		idx := (start + i + 1) % len(f.providers)
		provider := f.providers[idx]
		logger.Debug("Checking provider",
			"index", idx,
			"provider", provider.Name,
			"state", provider.State,
			"available", provider.IsAvailable())

		if provider.IsAvailable() {
			logger.Info("Switching to provider",
				"from_index", f.currentIndex,
				"to_index", idx,
				"provider", provider.Name,
				"state", provider.State)
			f.currentIndex = idx
			return provider, nil
		}
	}
	logger.Warn("No available providers found, attempting emergency recovery")
	return f.performEmergencyRecovery()
}

func (f *Failover[T]) performEmergencyRecovery() (*Provider, error) {
	if !f.config.EnableBlacklisting {
		return nil, fmt.Errorf("no available providers")
	}

	var blacklisted []*Provider
	for _, p := range f.providers {
		if p.State == StateBlacklisted {
			blacklisted = append(blacklisted, p)
		}
	}
	if len(blacklisted) == 0 {
		return nil, fmt.Errorf("no providers available")
	}

	sort.Slice(blacklisted, func(i, j int) bool {
		return blacklisted[i].BlacklistedUntil.Before(blacklisted[j].BlacklistedUntil)
	})

	firstRecovered := blacklisted[0]
	firstRecovered.Recover()
	f.currentIndex = 0
	logger.Info("Emergency recovery", "name", firstRecovered.Name)
	return firstRecovered, nil
}

// executeCore wraps fn with provider lifecycle (metrics, error analysis, blacklist)
func (f *Failover[T]) executeCore(_ context.Context, provider *Provider, fn func(T) error) error {
	client, ok := provider.Client.(T)
	if !ok {
		return fmt.Errorf(
			"provider client type mismatch: expected %T, got %T",
			*new(T),
			provider.Client,
		)
	}

	start := time.Now()
	err := fn(client)
	elapsed := time.Since(start)

	defer f.logProviderMetrics(provider, elapsed)

	if err != nil {
		issue := f.analyzeError(err, elapsed)
		if issue.MarkUnhealthy {
			logger.Warn("Switching provider due to error",
				"provider", provider.Name,
				"error", err.Error(),
				"blacklist_duration", issue.Cooldown,
			)
			provider.Blacklist(issue.Cooldown)
		} else {
			logger.Debug("Provider failed but not switching",
				"provider", provider.Name,
				"error", err.Error(),
				"consecutive_errors", provider.ConsecutiveErrors,
			)
			provider.Fail(&f.config)
		}
		return err
	}

	provider.Success(elapsed)
	return nil
}

// ExecuteWithRetry runs fn with automatic failover & retry
func (f *Failover[T]) ExecuteWithRetry(ctx context.Context, fn func(T) error) error {
	return retry.Constant(func() error {
		provider, err := f.GetBestProvider()
		if err != nil {
			return fmt.Errorf("no available provider: %w", err)
		}
		return f.executeCore(ctx, provider, fn)
	}, retry.DefaultInterval, retry.DefaultMaxAttempts)
}

func (f *Failover[T]) ExecuteWithRetryProvider(
	ctx context.Context,
	provider *Provider,
	fn func(T) error,
) error {
	return retry.Constant(func() error {
		return f.executeCore(ctx, provider, fn)
	}, retry.DefaultInterval, retry.DefaultMaxAttempts)
}

func (f *Failover[T]) logProviderMetrics(p *Provider, elapsed time.Duration) {
	now := time.Now()
	if now.Sub(f.lastHealthCheck) < f.config.HealthCheckInterval {
		return
	}
	f.lastHealthCheck = now

	statusEmoji := map[string]string{
		StateHealthy:     "✅",
		StateDegraded:    "⚠️",
		StateUnhealthy:   "❌",
		StateBlacklisted: "🚫",
	}[p.State]

	logger.Info("Provider metrics",
		"name", p.Name,
		"state", p.State,
		"emoji", statusEmoji,
		"latency_ms", elapsed.Milliseconds(),
		"avg_latency_ms", p.AverageResponseTime.Milliseconds(),
		"errors", p.ConsecutiveErrors,
	)
}

type ProviderIssue struct {
	Reason        string        // "rate_limit", "forbidden", etc.
	Detail        string        // full error message (for analysis/debug)
	Elapsed       time.Duration // request duration
	Cooldown      time.Duration // how long to backoff this provider
	MarkUnhealthy bool          // whether to mark provider unhealthy
}

func (f *Failover[T]) analyzeError(err error, elapsed time.Duration) ProviderIssue {
	msg := strings.ToLower(err.Error())
	issue := ProviderIssue{
		Reason:        "generic_error",
		Detail:        err.Error(),
		Elapsed:       elapsed,
		Cooldown:      0,
		MarkUnhealthy: false,
	}

	switch {
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "429"):
		issue.Reason = "rate_limit"
		issue.Cooldown = 5 * time.Minute
		issue.MarkUnhealthy = true

	case strings.Contains(msg, "forbidden"), strings.Contains(msg, "403"):
		issue.Reason = "forbidden"
		issue.Cooldown = 24 * time.Hour
		issue.MarkUnhealthy = true

	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		issue.Reason = "timeout"
		issue.Cooldown = 3 * time.Minute
		issue.MarkUnhealthy = true

	case strings.Contains(msg, "eof"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "connection refused"):
		issue.Reason = "connection_error"
		issue.Cooldown = 2 * time.Minute
		issue.MarkUnhealthy = true

	case elapsed > 3*time.Second:
		issue.Reason = "slow_response"
		issue.Cooldown = 2 * time.Minute
		issue.MarkUnhealthy = true
	}

	return issue
}

// Cleanup all providers
func (fm *Failover[T]) Cleanup() {
	for _, p := range fm.providers {
		if p.Client != nil {
			p.Client.Close()
		}
	}
}
