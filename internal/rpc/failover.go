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
	HealthCheckInterval  time.Duration
	EnableBlacklisting   bool
	MinActiveProviders   int
	ErrorThreshold       int
	ForceRotateThreshold int
	DefaultTimeout       time.Duration
	MaxBlacklistDuration time.Duration
	ProbeTimeout         time.Duration
}

func DefaultFailoverConfig() FailoverConfig {
	return FailoverConfig{
		HealthCheckInterval:  30 * time.Second,
		EnableBlacklisting:   true,
		MinActiveProviders:   2,
		ErrorThreshold:       5,
		ForceRotateThreshold: 3,
		DefaultTimeout:       10 * time.Second,
		MaxBlacklistDuration: 30 * time.Minute,
		ProbeTimeout:         5 * time.Second,
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

	probe       ProbeFunc[T]
	probeMaxLag uint64
}

// ProbeFunc checks a provider is serving and returns its chain head.
type ProbeFunc[T NetworkClient] func(ctx context.Context, client T) (uint64, error)

// SetProbe gates blacklist recovery on probe passing; call before the failover is used.
// With maxLag > 0, a probed provider whose head trails the current provider by more
// than maxLag stays blacklisted.
func (f *Failover[T]) SetProbe(probe ProbeFunc[T], maxLag uint64) {
	f.probe = probe
	f.probeMaxLag = maxLag
}

// NewFailover creates a new type-safe Failover[T]
func NewFailover[T NetworkClient](config *FailoverConfig) *Failover[T] {
	if config == nil {
		c := DefaultFailoverConfig()
		config = &c
	} else {
		c := *config
		config = &c
	}
	defaults := DefaultFailoverConfig()
	if config.ForceRotateThreshold <= 0 {
		config.ForceRotateThreshold = defaults.ForceRotateThreshold
	}
	if config.MaxBlacklistDuration <= 0 {
		config.MaxBlacklistDuration = defaults.MaxBlacklistDuration
	}
	if config.ProbeTimeout <= 0 {
		config.ProbeTimeout = defaults.ProbeTimeout
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
		if cur.Backup {
			if p, ok := f.switchToPrimary(curIdx); ok {
				return p, nil
			}
		}
		if cur.IsAvailable() {
			cur.mu.RLock()
			consecutiveErrors := cur.ConsecutiveErrors
			cur.mu.RUnlock()
			if consecutiveErrors < f.config.ForceRotateThreshold {
				return cur, nil
			}
		}

		if f.logThrottler.ShouldLog(fmt.Sprintf("unavailable_%s", cur.Name)) {
			cur.mu.RLock()
			curURL := cur.URL
			state := cur.State
			blacklistedUntil := cur.BlacklistedUntil
			consecutiveErrors := cur.ConsecutiveErrors
			cur.mu.RUnlock()

			logger.Warn("Current provider not available, finding alternative",
				"provider", cur.Name,
				"url", curURL,
				"state", state,
				"blacklisted_until", blacklistedUntil.Format(time.RFC3339),
				"consecutive_errors", consecutiveErrors,
				"force_rotate_threshold", f.config.ForceRotateThreshold)
		}
	}

	return f.findNextAvailableProvider()
}

// switchToPrimary moves off a backup provider as soon as any primary is available again.
func (f *Failover[T]) switchToPrimary(fromIdx int) (*Provider, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.currentIndex != fromIdx {
		return nil, false
	}
	for idx, p := range f.providers {
		if !p.Backup && p.IsAvailable() {
			logger.Info("Returning to primary provider",
				"from_provider", f.providers[fromIdx].Name,
				"to_provider", p.Name)
			f.currentIndex = idx
			f.metrics.IncrementSwitch()
			return p, true
		}
	}
	return nil, false
}

// recoverExpiredBlacklists recovers expired blacklists, or starts a background
// probe for providers that must pass one first.
func (f *Failover[T]) recoverExpiredBlacklists(providers []*Provider) {
	for _, p := range providers {
		if !p.IsExpiredBlacklist() {
			continue
		}
		if f.probe != nil && p.RequiresProbe() {
			if p.tryStartProbe() {
				go f.runProbe(p)
			}
			continue
		}
		logger.Info("Recovering expired blacklisted provider", "provider", p.Name)
		p.Recover()
		f.metrics.IncrementRecovery()
	}
}

func (f *Failover[T]) runProbe(p *Provider) {
	defer p.endProbe()

	ctx, cancel := context.WithTimeout(context.Background(), f.config.ProbeTimeout)
	defer cancel()

	if err := f.checkProvider(ctx, p); err != nil {
		d := p.ReblacklistEscalating(f.config.MaxBlacklistDuration)
		f.metrics.IncrementBlacklist()
		logger.Warn("Recovery probe failed, keeping provider blacklisted",
			"provider", p.Name,
			"error", err.Error(),
			"blacklist_duration", d)
		return
	}

	logger.Info("Recovery probe passed, recovering provider", "provider", p.Name)
	p.Recover()
	f.metrics.IncrementRecovery()
}

func (f *Failover[T]) checkProvider(ctx context.Context, p *Provider) error {
	client, ok := p.Client.(T)
	if !ok {
		return fmt.Errorf("provider client type mismatch: expected %T, got %T", *new(T), p.Client)
	}
	head, err := f.probe(ctx, client)
	if err != nil {
		return err
	}
	if f.probeMaxLag == 0 {
		return nil
	}

	f.mu.RLock()
	var ref *Provider
	if f.currentIndex >= 0 && f.currentIndex < len(f.providers) {
		ref = f.providers[f.currentIndex]
	}
	f.mu.RUnlock()
	if ref == nil || ref == p || !ref.IsAvailable() {
		return nil
	}

	refClient, ok := ref.Client.(T)
	if !ok {
		return nil
	}
	// A failing reference says nothing about p, so don't hold p back for it.
	refHead, err := f.probe(ctx, refClient)
	if err != nil {
		return nil
	}
	if refHead > head && refHead-head > f.probeMaxLag {
		return fmt.Errorf("head %d lags %s by %d blocks", head, ref.Name, refHead-head)
	}
	return nil
}

// GetAvailableProviders returns all currently available primary providers,
// or the available backups when no primary is available.
func (f *Failover[T]) GetAvailableProviders() []*Provider {
	f.mu.RLock()
	providers := append([]*Provider(nil), f.providers...)
	f.mu.RUnlock()

	f.recoverExpiredBlacklists(providers)

	var primaries, backups []*Provider
	for _, p := range providers {
		if !p.IsAvailable() {
			continue
		}
		if p.Backup {
			backups = append(backups, p)
		} else {
			primaries = append(primaries, p)
		}
	}
	if len(primaries) > 0 {
		return primaries
	}
	return backups
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

	// Primaries first, backups only when no primary is available.
	for _, wantBackup := range []bool{false, true} {
		for i := 0; i < len(f.providers); i++ {
			idx := (start + i + 1) % len(f.providers)
			provider := f.providers[idx]
			if provider.Backup != wantBackup {
				continue
			}

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
					"backup", provider.Backup,
					"state", provider.State)

				f.currentIndex = idx
				f.metrics.IncrementSwitch()
				return provider, nil
			}
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
	for idx, p := range f.providers {
		if p == first {
			f.currentIndex = idx
			break
		}
	}
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
		if issue.Reason == "generic_error" {
			logger.Warn("Unknown provider error type", "provider", provider.Name, "error", issue.Detail)
			f.metrics.IncrementErrorType("unknown_error_type")
		}

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
	cooldown := provider.BlacklistEscalating(issue.Cooldown, f.config.MaxBlacklistDuration, f.probe != nil)
	f.metrics.IncrementBlacklist()

	logKey := fmt.Sprintf("switch_%s", provider.Name)
	if f.logThrottler.ShouldLog(logKey) {
		provider.mu.RLock()
		providerURL := provider.URL
		consecutiveErrors := provider.ConsecutiveErrors
		strikes := provider.BlacklistStrikes
		provider.mu.RUnlock()

		logger.Warn("Switching provider due to error",
			"provider", provider.Name,
			"url", providerURL,
			"error_type", issue.Reason,
			"error_detail", issue.Detail,
			"consecutive_errors", consecutiveErrors+1,
			"blacklist_strikes", strikes,
			"blacklist_duration", cooldown,
		)
	}
}

// handleProviderFailure handles non-blacklist failures
func (f *Failover[T]) handleProviderFailure(provider *Provider, err error) {
	provider.mu.RLock()
	providerURL := provider.URL
	state := provider.State
	consecutiveErrors := provider.ConsecutiveErrors
	provider.mu.RUnlock()

	logger.Debug("Provider failed but not switching",
		"provider", provider.Name,
		"url", providerURL,
		"state", state,
		"error", err.Error(),
		"consecutive_errors", consecutiveErrors,
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
		current.mu.RLock()
		blacklistedUntil := current.BlacklistedUntil
		consecutiveErrors := current.ConsecutiveErrors
		state := current.State
		currentURL := current.URL
		current.mu.RUnlock()

		alt.mu.RLock()
		altURL := alt.URL
		altState := alt.State
		alt.mu.RUnlock()

		logger.Warn("Provider already blacklisted, switching immediately",
			"from_provider", current.Name,
			"from_url", currentURL,
			"from_state", state,
			"blacklisted_until", blacklistedUntil.Format(time.RFC3339),
			"consecutive_errors", consecutiveErrors,
			"to_provider", alt.Name,
			"to_url", altURL,
			"to_state", altState)
	}

	return alt
}

// attemptFallback tries to fallback to an alternative provider
func (f *Failover[T]) attemptFallback(current *Provider, err error) *Provider {
	alt, altErr := f.GetBestProvider()
	if altErr != nil || alt.Name == current.Name {
		return nil
	}

	current.mu.RLock()
	currentURL := current.URL
	currentState := current.State
	consecutiveErrors := current.ConsecutiveErrors
	current.mu.RUnlock()

	alt.mu.RLock()
	altURL := alt.URL
	altState := alt.State
	alt.mu.RUnlock()

	logger.Warn("Fallback to alternative provider",
		"from_provider", current.Name,
		"from_url", currentURL,
		"from_state", currentState,
		"consecutive_errors", consecutiveErrors,
		"to_provider", alt.Name,
		"to_url", altURL,
		"to_state", altState,
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

	// Read provider fields under the provider's lock to avoid data races
	// when called concurrently from multiple goroutines (e.g., fetchTraces).
	p.mu.RLock()
	state := p.State
	avgResponseTime := p.AverageResponseTime
	consecutiveErrors := p.ConsecutiveErrors
	p.mu.RUnlock()

	statusEmoji := map[string]string{
		StateHealthy:     "✅",
		StateDegraded:    "⚠️",
		StateUnhealthy:   "❌",
		StateBlacklisted: "🚫",
	}[state]

	logger.Info("Provider metrics",
		"name", p.Name,
		"state", state,
		"emoji", statusEmoji,
		"latency_ms", elapsed.Milliseconds(),
		"avg_latency_ms", avgResponseTime.Milliseconds(),
		"errors", consecutiveErrors,
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
			patterns: []string{
				"-32701",
				"-32603",
				"-32612",
				"-32613",
				"please specify an address",
				"remove restrictions",
				"order a dedicated full node",
			},
			reason:        "restricted_query",
			cooldown:      5 * time.Minute,
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
			patterns:      []string{"tls: internal error", "tls handshake", "handshake failure", "remote error: tls"},
			reason:        "tls_error",
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

// AnalyzeAndHandleError classifies an error and applies the same blacklist/fail
// policy as executeCore. Also updates failover metrics so trace-pool
// request/failure totals appear in GetMetrics(). Use this when calling provider
// clients directly (outside the retry wrapper) but still wanting consistent
// error handling and observability.
func (f *Failover[T]) AnalyzeAndHandleError(provider *Provider, err error, elapsed time.Duration) {
	f.metrics.IncrementTotal()
	f.metrics.IncrementProviderRequest(provider.Name)
	f.metrics.IncrementFailure()

	issue := f.analyzeError(err, elapsed)
	f.metrics.IncrementErrorType(issue.Reason)
	if issue.Reason == "generic_error" {
		logger.Warn("Unknown provider error type", "provider", provider.Name, "error", issue.Detail)
		f.metrics.IncrementErrorType("unknown_error_type")
	}

	if issue.MarkUnhealthy {
		f.handleUnhealthyProvider(provider, issue)
	} else {
		f.handleProviderFailure(provider, err)
	}

	f.logProviderMetrics(provider, elapsed)
}

// RecordSuccess updates provider health state and metrics for a successful direct call.
// Mirrors executeCore's success path: provider.Success(elapsed) + metric increments.
func (f *Failover[T]) RecordSuccess(provider *Provider, elapsed time.Duration) {
	provider.Success(elapsed)
	f.metrics.IncrementTotal()
	f.metrics.IncrementProviderRequest(provider.Name)
	f.metrics.IncrementSuccess()
	f.logProviderMetrics(provider, elapsed)
}

// HandleCapabilityError blacklists a provider for a given cooldown AND records
// metrics. Use for permanent capability errors like "method not found"
// where the cooldown differs from what analyzeError would assign.
// Caller is responsible for logging the error before calling this.
func (f *Failover[T]) HandleCapabilityError(provider *Provider, elapsed time.Duration, cooldown time.Duration) {
	f.metrics.IncrementTotal()
	f.metrics.IncrementProviderRequest(provider.Name)
	f.metrics.IncrementFailure()
	f.metrics.IncrementErrorType("capability_error")
	provider.Blacklist(cooldown)
	f.metrics.IncrementBlacklist()
	f.logProviderMetrics(provider, elapsed)
}

// ProviderIssue represents an analyzed error state from a provider
type ProviderIssue struct {
	Reason        string
	Detail        string
	Elapsed       time.Duration
	Cooldown      time.Duration
	MarkUnhealthy bool
}
