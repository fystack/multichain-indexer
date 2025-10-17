package rpc

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/retry"
)

// FailoverConfig defines runtime behavior of the failover system.
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

// FailoverMetrics tracks operational metrics
type FailoverMetrics struct {
	mu                    sync.RWMutex
	TotalRequests         int64
	SuccessfulRequests    int64
	FailedRequests        int64
	ProviderSwitches      int64
	BlacklistEvents       int64
	RecoveryEvents        int64
	EmergencyRecoveries   int64
	FallbackAttempts      int64
	ErrorsByType          map[string]int64
	ProviderRequestCounts map[string]int64
}

func NewFailoverMetrics() *FailoverMetrics {
	return &FailoverMetrics{
		ErrorsByType:          make(map[string]int64),
		ProviderRequestCounts: make(map[string]int64),
	}
}

func (m *FailoverMetrics) IncrementTotal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalRequests++
}

func (m *FailoverMetrics) IncrementSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SuccessfulRequests++
}

func (m *FailoverMetrics) IncrementFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailedRequests++
}

func (m *FailoverMetrics) IncrementSwitch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProviderSwitches++
}

func (m *FailoverMetrics) IncrementBlacklist() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BlacklistEvents++
}

func (m *FailoverMetrics) IncrementRecovery() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RecoveryEvents++
}

func (m *FailoverMetrics) IncrementEmergencyRecovery() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EmergencyRecoveries++
}

func (m *FailoverMetrics) IncrementFallback() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FallbackAttempts++
}

func (m *FailoverMetrics) IncrementErrorType(errorType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ErrorsByType[errorType]++
}

func (m *FailoverMetrics) IncrementProviderRequest(providerName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProviderRequestCounts[providerName]++
}

func (m *FailoverMetrics) GetSnapshot() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	errorsByType := make(map[string]int64)
	for k, v := range m.ErrorsByType {
		errorsByType[k] = v
	}

	providerCounts := make(map[string]int64)
	for k, v := range m.ProviderRequestCounts {
		providerCounts[k] = v
	}

	return map[string]interface{}{
		"total_requests":          m.TotalRequests,
		"successful_requests":     m.SuccessfulRequests,
		"failed_requests":         m.FailedRequests,
		"provider_switches":       m.ProviderSwitches,
		"blacklist_events":        m.BlacklistEvents,
		"recovery_events":         m.RecoveryEvents,
		"emergency_recoveries":    m.EmergencyRecoveries,
		"fallback_attempts":       m.FallbackAttempts,
		"errors_by_type":          errorsByType,
		"provider_request_counts": providerCounts,
	}
}

// LogThrottler prevents log spam by rate-limiting similar log messages
type LogThrottler struct {
	mu            sync.RWMutex
	lastLogTimes  map[string]time.Time
	throttleDelay time.Duration
}

func NewLogThrottler(delay time.Duration) *LogThrottler {
	return &LogThrottler{
		lastLogTimes:  make(map[string]time.Time),
		throttleDelay: delay,
	}
}

func (lt *LogThrottler) ShouldLog(key string) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	now := time.Now()
	lastTime, exists := lt.lastLogTimes[key]

	if !exists || now.Sub(lastTime) > lt.throttleDelay {
		lt.lastLogTimes[key] = now
		return true
	}

	return false
}

func (lt *LogThrottler) Reset(key string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.lastLogTimes, key)
}

// Failover manages multiple providers for a specific client type T
type Failover[T NetworkClient] struct {
	mu              sync.RWMutex
	providers       []*Provider
	currentIndex    int
	config          FailoverConfig
	lastHealthCheck time.Time
	metrics         *FailoverMetrics
	logThrottler    *LogThrottler
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
		metrics:      NewFailoverMetrics(),
		logThrottler: NewLogThrottler(30 * time.Second),
	}
}

// GetMetrics returns a snapshot of current metrics
func (f *Failover[T]) GetMetrics() map[string]interface{} {
	return f.metrics.GetSnapshot()
}

// AddProvider adds a provider, ensuring its Client is of type T
func (f *Failover[T]) AddProvider(p *Provider) error {
	if _, ok := p.Client.(T); !ok {
		return fmt.Errorf("invalid provider client type: expected %T, got %T", *new(T), p.Client)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.providers = append(f.providers, p)
	if f.currentIndex == -1 {
		f.currentIndex = 0
	}
	logger.Info("Added provider", "name", p.Name, "url", p.URL)
	return nil
}

// GetBestProvider returns the current best provider
func (f *Failover[T]) GetBestProvider() (*Provider, error) {
	f.mu.RLock()
	if len(f.providers) == 0 {
		f.mu.RUnlock()
		return nil, fmt.Errorf("no providers configured")
	}
	providers := append([]*Provider(nil), f.providers...)
	curIdx := f.currentIndex
	f.mu.RUnlock()

	// Check for expired blacklists and recover
	f.recoverExpiredBlacklists(providers)

	if curIdx >= 0 && curIdx < len(providers) {
		cur := providers[curIdx]
		if cur.IsAvailable() {
			return cur, nil
		}

		if f.logThrottler.ShouldLog(fmt.Sprintf("unavailable_%s", cur.Name)) {
			logger.Warn("Current provider not available, finding alternative",
				"provider", cur.Name,
				"state", cur.State,
				"consecutive_errors", cur.ConsecutiveErrors)
		}
	}

	return f.findNextAvailableProvider()
}

// recoverExpiredBlacklists checks and recovers any expired blacklisted providers
func (f *Failover[T]) recoverExpiredBlacklists(providers []*Provider) {
	for _, p := range providers {
		if p.IsExpiredBlacklist() {
			logger.Info("Recovering expired blacklisted provider", "provider", p.Name)
			p.Recover()
			f.metrics.IncrementRecovery()
		}
	}
}

// GetAvailableProviders returns all currently available providers
func (f *Failover[T]) GetAvailableProviders() []*Provider {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var available []*Provider
	for _, p := range f.providers {
		if p.IsExpiredBlacklist() {
			p.Recover()
			f.metrics.IncrementRecovery()
		}
		if p.IsAvailable() {
			available = append(available, p)
		}
	}
	return available
}

// findNextAvailableProvider finds the next usable provider and updates currentIndex
func (f *Failover[T]) findNextAvailableProvider() (*Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	start := f.currentIndex
	logger.Info("Searching for available provider",
		"start_index", start,
		"total_providers", len(f.providers))

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
			f.metrics.IncrementSwitch()
			return provider, nil
		}
	}

	logger.Warn("No available providers found, attempting emergency recovery")
	return f.performEmergencyRecoveryLocked()
}

// performEmergencyRecoveryLocked assumes f.mu is already held (exclusive)
func (f *Failover[T]) performEmergencyRecoveryLocked() (*Provider, error) {
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

	// Sort by blacklist expiry time (earliest first)
	sort.Slice(blacklisted, func(i, j int) bool {
		return blacklisted[i].BlacklistedUntil.Before(blacklisted[j].BlacklistedUntil)
	})

	first := blacklisted[0]
	first.Recover()
	f.currentIndex = 0
	f.metrics.IncrementEmergencyRecovery()

	logger.Info("Emergency recovery", "name", first.Name)
	return first, nil
}

// executeCore wraps fn with provider lifecycle (metrics, error analysis, blacklist)
func (f *Failover[T]) executeCore(ctx context.Context, provider *Provider, fn func(T) error) error {
	client, ok := provider.Client.(T)
	if !ok {
		return fmt.Errorf("provider client type mismatch: expected %T, got %T", *new(T), provider.Client)
	}

	f.metrics.IncrementTotal()
	f.metrics.IncrementProviderRequest(provider.Name)

	start := time.Now()
	err := fn(client)
	elapsed := time.Since(start)

	defer f.logProviderMetrics(provider, elapsed)

	if err != nil {
		f.metrics.IncrementFailure()
		issue := f.analyzeError(err, elapsed)
		f.metrics.IncrementErrorType(issue.Reason)

		if issue.MarkUnhealthy {
			f.handleUnhealthyProvider(provider, issue)
		} else {
			f.handleProviderFailure(provider, err)
		}
		return err
	}

	f.metrics.IncrementSuccess()
	provider.Success(elapsed)
	return nil
}

// handleUnhealthyProvider marks provider as unhealthy and blacklists it
func (f *Failover[T]) handleUnhealthyProvider(provider *Provider, issue ProviderIssue) {
	logKey := fmt.Sprintf("switch_%s", provider.Name)
	if f.logThrottler.ShouldLog(logKey) {
		logger.Warn("Switching provider due to error",
			"provider", provider.Name,
			"error_type", issue.Reason,
			"blacklist_duration", issue.Cooldown,
		)
	}

	provider.Blacklist(issue.Cooldown)
	f.metrics.IncrementBlacklist()
}

// handleProviderFailure handles non-blacklist failures
func (f *Failover[T]) handleProviderFailure(provider *Provider, err error) {
	logger.Debug("Provider failed but not switching",
		"provider", provider.Name,
		"error", err.Error(),
		"consecutive_errors", provider.ConsecutiveErrors,
	)
	provider.Fail(&f.config)
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

// ExecuteWithRetryProvider runs fn against a specific provider with optional fallback
func (f *Failover[T]) ExecuteWithRetryProvider(
	ctx context.Context,
	provider *Provider,
	fn func(T) error,
	allowFallback bool,
) error {
	return retry.Constant(func() error {
		// Check if provider is already blacklisted
		if provider.State == StateBlacklisted && allowFallback {
			if alt := f.attemptProviderSwitch(provider); alt != nil {
				provider = alt
			}
		}

		err := f.executeCore(ctx, provider, fn)
		if err != nil && allowFallback {
			// Skip fallback if already blacklisted
			if provider.State == StateBlacklisted {
				return err
			}

			if alt := f.attemptFallback(provider, err); alt != nil {
				f.metrics.IncrementFallback()
				time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
				return f.executeCore(ctx, alt, fn)
			}
		}
		return err
	}, retry.DefaultInterval, retry.DefaultMaxAttempts)
}

// attemptProviderSwitch tries to switch from a blacklisted provider
func (f *Failover[T]) attemptProviderSwitch(current *Provider) *Provider {
	alt, err := f.GetBestProvider()
	if err != nil || alt.Name == current.Name {
		return nil
	}

	logKey := fmt.Sprintf("blacklist_%s", current.Name)
	if f.logThrottler.ShouldLog(logKey) {
		logger.Warn("Provider already blacklisted, switching immediately",
			"from", current.Name,
			"to", alt.Name)
	}

	return alt
}

// attemptFallback tries to fallback to an alternative provider
func (f *Failover[T]) attemptFallback(current *Provider, err error) *Provider {
	alt, altErr := f.GetBestProvider()
	if altErr != nil || alt.Name == current.Name {
		return nil
	}

	logger.Warn("Fallback to alternative provider",
		"from", current.Name,
		"to", alt.Name,
		"error", err.Error())

	return alt
}

// logProviderMetrics periodically logs provider performance and state
func (f *Failover[T]) logProviderMetrics(p *Provider, elapsed time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

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

// analyzeError determines error type and suggests cooldown policy
func (f *Failover[T]) analyzeError(err error, elapsed time.Duration) ProviderIssue {
	msg := strings.ToLower(err.Error())
	issue := ProviderIssue{
		Reason:        "generic_error",
		Detail:        err.Error(),
		Elapsed:       elapsed,
		Cooldown:      0,
		MarkUnhealthy: false,
	}

	errorPatterns := []struct {
		patterns      []string
		reason        string
		cooldown      time.Duration
		markUnhealthy bool
	}{
		{
			patterns:      []string{"rate limit", "429", "too many requests"},
			reason:        "rate_limit",
			cooldown:      5 * time.Minute,
			markUnhealthy: true,
		},
		{
			patterns:      []string{"-32001", "exceeded the quota", "quota usage", "quota limit"},
			reason:        "quota_exceeded",
			cooldown:      5 * time.Minute,
			markUnhealthy: true,
		},
		{
			patterns:      []string{"forbidden", "403"},
			reason:        "forbidden",
			cooldown:      24 * time.Hour,
			markUnhealthy: true,
		},
		{
			patterns:      []string{"timeout", "deadline"},
			reason:        "timeout",
			cooldown:      3 * time.Minute,
			markUnhealthy: true,
		},
		{
			patterns:      []string{"eof", "connection reset", "connection refused", "broken pipe"},
			reason:        "connection_error",
			cooldown:      2 * time.Minute,
			markUnhealthy: true,
		},
		{
			patterns:      []string{"-32007", "batch limit exceeded", "internal error -32005"},
			reason:        "batch_limit",
			cooldown:      1 * time.Minute,
			markUnhealthy: true,
		},
	}

	for _, pattern := range errorPatterns {
		for _, p := range pattern.patterns {
			if strings.Contains(msg, p) {
				issue.Reason = pattern.reason
				issue.Cooldown = pattern.cooldown
				issue.MarkUnhealthy = pattern.markUnhealthy
				return issue
			}
		}
	}

	// Check for slow response
	if elapsed > 3*time.Second {
		issue.Reason = "slow_response"
		issue.Cooldown = 2 * time.Minute
		issue.MarkUnhealthy = true
	}

	return issue
}

// ProviderIssue represents an analyzed error state from a provider
type ProviderIssue struct {
	Reason        string
	Detail        string
	Elapsed       time.Duration
	Cooldown      time.Duration
	MarkUnhealthy bool
}
