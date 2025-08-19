package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/common/ratelimiter"
	"github.com/fystack/transaction-indexer/internal/common/retry"
)

// Provider represents a blockchain network provider
type Provider struct {
	Name       string        `json:"name"`
	URL        string        `json:"url"`
	Network    string        `json:"network"`     // evm, solana, tron, etc.
	ClientType string        `json:"client_type"` // rpc, rest
	Auth       *AuthConfig   `json:"auth,omitempty"`
	Client     NetworkClient `json:"-"`

	// Health metrics
	State               string        `json:"state"`
	LastHealthCheck     time.Time     `json:"last_health_check"`
	AverageResponseTime time.Duration `json:"average_response_time"`
	BlacklistedUntil    time.Time     `json:"blacklisted_until,omitempty"`
	ConsecutiveErrors   int           `json:"consecutive_errors"`
}

func (p *Provider) IsAvailable() bool {
	now := time.Now()
	return p.State != StateBlacklisted || now.After(p.BlacklistedUntil)
}

func (p *Provider) IsExpiredBlacklist() bool {
	return p.State == StateBlacklisted && time.Now().After(p.BlacklistedUntil)
}

// ProviderIssue represents an error analysis result
type ProviderIssue struct {
	ErrorType         string        `json:"error_type"`
	BlacklistDuration time.Duration `json:"blacklist_duration"`
	ShouldSwitch      bool          `json:"should_switch"`
}

// FailoverConfig holds configuration for the failover manager
type FailoverConfig struct {
	HealthCheckInterval time.Duration `json:"health_check_interval"`
	EnableBlacklisting  bool          `json:"enable_blacklisting"`
	MinActiveProviders  int           `json:"min_active_providers"`
	ErrorThreshold      int           `json:"error_threshold"`
	DefaultTimeout      time.Duration `json:"default_timeout"`
}

func DefaultFailoverConfig() *FailoverConfig {
	return &FailoverConfig{
		HealthCheckInterval: 30 * time.Second,
		EnableBlacklisting:  true,
		MinActiveProviders:  2,
		ErrorThreshold:      5,
		DefaultTimeout:      10 * time.Second,
	}
}

// FailoverManager manages multiple blockchain providers with automatic failover
type FailoverManager struct {
	providers       []*Provider
	currentIndex    int
	config          *FailoverConfig
	lastHealthCheck time.Time
	mutex           sync.RWMutex
}

func NewFailoverManager(config *FailoverConfig) *FailoverManager {
	if config == nil {
		config = DefaultFailoverConfig()
	}

	return &FailoverManager{
		providers:       make([]*Provider, 0),
		currentIndex:    -1,
		config:          config,
		lastHealthCheck: time.Now(),
	}
}

// AddProvider adds a new provider to the manager
func (fm *FailoverManager) AddProvider(name, url, network, clientType string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	// Check for duplicates
	for _, provider := range fm.providers {
		if provider.Name == name || provider.URL == url {
			return fmt.Errorf("provider with name '%s' or URL '%s' already exists", name, url)
		}
	}

	// Create generic client
	client := NewGenericClient(url, network, clientType, auth, fm.config.DefaultTimeout, rateLimiter)

	provider := &Provider{
		Name:            name,
		URL:             url,
		Network:         network,
		ClientType:      clientType,
		Auth:            auth,
		Client:          client,
		State:           StateHealthy,
		LastHealthCheck: time.Now(),
	}

	fm.providers = append(fm.providers, provider)

	// Set as current if it's the first provider
	if fm.currentIndex == -1 {
		fm.currentIndex = 0
	}
	attrs := []any{
		"name", name,
		"url", url,
		"network", network,
		"client_type", clientType,
		"total_providers", len(fm.providers),
	}

	if auth != nil {
		attrs = append(attrs, "auth", auth.Type)
	}

	slog.With(attrs...).Info("Added provider")

	return nil
}

// GetBestProvider returns the best available provider
func (fm *FailoverManager) GetBestProvider() (*Provider, error) {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	if len(fm.providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	fm.updateExpiredBlacklists()

	// Try current provider first
	if fm.currentIndex >= 0 && fm.currentIndex < len(fm.providers) {
		current := fm.providers[fm.currentIndex]
		if current.IsAvailable() {
			return current, nil
		}
	}

	// Find next available provider using round-robin
	return fm.findNextAvailableProvider()
}

func (fm *FailoverManager) updateExpiredBlacklists() {
	// now := time.Now()
	for _, provider := range fm.providers {
		if provider.IsExpiredBlacklist() {
			provider.State = StateDegraded
			provider.BlacklistedUntil = time.Time{}
			provider.ConsecutiveErrors = 0

			slog.Info("Provider blacklist expired",
				"name", provider.Name,
				"url", provider.URL)
		}
	}
}

func (fm *FailoverManager) findNextAvailableProvider() (*Provider, error) {
	startIndex := fm.currentIndex

	for i := 0; i < len(fm.providers); i++ {
		candidateIndex := (startIndex + i + 1) % len(fm.providers)
		candidate := fm.providers[candidateIndex]

		if candidate.IsAvailable() {
			fm.currentIndex = candidateIndex

			slog.Info("Switched to provider",
				"name", candidate.Name,
				"url", candidate.URL,
				"reason", "previous provider unavailable")

			return candidate, nil
		}
	}

	// All providers blacklisted - emergency recovery
	return fm.performEmergencyRecovery()
}

func (fm *FailoverManager) performEmergencyRecovery() (*Provider, error) {
	if !fm.config.EnableBlacklisting {
		return nil, fmt.Errorf("no available providers")
	}

	minProviders := fm.config.MinActiveProviders
	if minProviders <= 0 || minProviders > len(fm.providers) {
		minProviders = 1
	}

	// Find providers with earliest blacklist expiry
	blacklisted := make([]*Provider, 0)
	for _, provider := range fm.providers {
		if provider.State == StateBlacklisted {
			blacklisted = append(blacklisted, provider)
		}
	}

	if len(blacklisted) == 0 {
		return nil, fmt.Errorf("no providers available")
	}

	// Sort by blacklist expiry time (earliest first)
	for i := 0; i < len(blacklisted)-1; i++ {
		for j := i + 1; j < len(blacklisted); j++ {
			if blacklisted[j].BlacklistedUntil.Before(blacklisted[i].BlacklistedUntil) {
				blacklisted[i], blacklisted[j] = blacklisted[j], blacklisted[i]
			}
		}
	}

	// Unblacklist required number of providers
	recoveredCount := 0
	var firstRecovered *Provider

	for _, provider := range blacklisted {
		if recoveredCount >= minProviders {
			break
		}

		provider.State = StateDegraded
		provider.BlacklistedUntil = time.Time{}
		provider.ConsecutiveErrors = 0

		if firstRecovered == nil {
			firstRecovered = provider
		}

		recoveredCount++

		slog.Info("Emergency recovery: unblacklisted provider",
			"name", provider.Name,
			"url", provider.URL)
	}

	if firstRecovered != nil {
		// Find index of recovered provider
		for i, provider := range fm.providers {
			if provider == firstRecovered {
				fm.currentIndex = i
				break
			}
		}
	}

	return firstRecovered, nil
}

// ExecuteWithRetry executes an action with automatic provider switching and retry logic
func (fm *FailoverManager) ExecuteWithRetry(ctx context.Context, action func(NetworkClient) error) error {
	return retry.Constant(func() error {
		provider, err := fm.GetBestProvider()
		if err != nil {
			return fmt.Errorf("no available provider: %w", err)
		}

		// Execute action with timing
		start := time.Now()
		err = action(provider.Client)
		elapsed := time.Since(start)

		// Record metrics and handle errors
		fm.recordMetrics(provider, elapsed, err)

		if err != nil {
			issue := fm.analyzeError(err, elapsed)

			if issue.ShouldSwitch {
				fm.handleProviderIssue(provider, issue)
			}

			return err
		}

		// Reset consecutive errors on success
		fm.mutex.Lock()
		provider.ConsecutiveErrors = 0
		fm.mutex.Unlock()

		return nil

	}, 5*time.Second, retry.DefaultMaxAttempts)
}

func (fm *FailoverManager) recordMetrics(provider *Provider, elapsed time.Duration, err error) {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	provider.LastHealthCheck = time.Now()

	// Update average response time using exponential moving average
	if provider.AverageResponseTime == 0 {
		provider.AverageResponseTime = elapsed
	} else {
		// 80% old value, 20% new value
		provider.AverageResponseTime = time.Duration(
			float64(provider.AverageResponseTime)*0.8 + float64(elapsed)*0.2)
	}

	// Update state based on performance and errors
	if err == nil {
		provider.ConsecutiveErrors = 0
		fm.updateHealthState(provider, elapsed)
	} else {
		if provider.State != StateBlacklisted {
			provider.ConsecutiveErrors++
			fm.updateErrorState(provider)
		}
	}

	fm.logMetricsIfNeeded(provider, elapsed)
}

func (fm *FailoverManager) updateHealthState(provider *Provider, elapsed time.Duration) {
	switch {
	case elapsed < 300*time.Millisecond:
		provider.State = StateHealthy
	case elapsed < 1*time.Second:
		provider.State = StateDegraded
	default:
		provider.State = StateUnhealthy
	}
}

func (fm *FailoverManager) updateErrorState(provider *Provider) {
	switch {
	case provider.ConsecutiveErrors >= fm.config.ErrorThreshold:
		provider.State = StateUnhealthy
	case provider.ConsecutiveErrors >= 2:
		provider.State = StateDegraded
	}
}

func (fm *FailoverManager) logMetricsIfNeeded(provider *Provider, elapsed time.Duration) {
	now := time.Now()
	if now.Sub(fm.lastHealthCheck) > fm.config.HealthCheckInterval {
		fm.lastHealthCheck = now

		statusEmoji := fm.getStatusEmoji(provider.State)

		slog.Info("Provider metrics",
			"name", provider.Name,
			"network", provider.Network,
			"client_type", provider.ClientType,
			"status", provider.State,
			"emoji", statusEmoji,
			"latency_ms", elapsed.Milliseconds(),
			"avg_latency_ms", provider.AverageResponseTime.Milliseconds(),
			"consecutive_errors", provider.ConsecutiveErrors)
	}
}

func (fm *FailoverManager) getStatusEmoji(state string) string {
	switch state {
	case StateHealthy:
		return "✅"
	case StateDegraded:
		return "⚠️"
	case StateUnhealthy:
		return "❌"
	case StateBlacklisted:
		return "🚫"
	default:
		return "❓"
	}
}

func (fm *FailoverManager) analyzeError(err error, elapsed time.Duration) *ProviderIssue {
	errorMsg := strings.ToLower(err.Error())

	switch {
	case strings.Contains(errorMsg, "rate limit") || strings.Contains(errorMsg, "429") || strings.Contains(errorMsg, "quota"):
		return &ProviderIssue{
			ErrorType:         "rate_limit",
			BlacklistDuration: 5 * time.Minute,
			ShouldSwitch:      true,
		}

	case strings.Contains(errorMsg, "forbidden") || strings.Contains(errorMsg, "403"):
		return &ProviderIssue{
			ErrorType:         "forbidden",
			BlacklistDuration: 24 * time.Hour,
			ShouldSwitch:      true,
		}

	case strings.Contains(errorMsg, "timeout") || strings.Contains(errorMsg, "deadline"):
		return &ProviderIssue{
			ErrorType:         "timeout",
			BlacklistDuration: 3 * time.Minute,
			ShouldSwitch:      true,
		}

	case elapsed > 3*time.Second:
		return &ProviderIssue{
			ErrorType:         "slow_response",
			BlacklistDuration: 2 * time.Minute,
			ShouldSwitch:      true,
		}

	default:
		return &ProviderIssue{
			ErrorType:    "generic_error",
			ShouldSwitch: false,
		}
	}
}

func (fm *FailoverManager) handleProviderIssue(provider *Provider, issue *ProviderIssue) {
	if !fm.config.EnableBlacklisting {
		slog.Warn("Provider issue detected but blacklisting disabled",
			"name", provider.Name,
			"error_type", issue.ErrorType)
		return
	}

	// Check if blacklisting would drop active providers below minimum
	activeCount := fm.countActiveProviders()
	if activeCount <= fm.config.MinActiveProviders {
		slog.Warn("Not blacklisting provider: would drop below minimum active count",
			"name", provider.Name,
			"active_count", activeCount,
			"min_required", fm.config.MinActiveProviders)
		return
	}

	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	provider.State = StateBlacklisted
	provider.BlacklistedUntil = time.Now().Add(issue.BlacklistDuration)

	slog.Info("Blacklisted provider",
		"name", provider.Name,
		"error_type", issue.ErrorType,
		"duration", issue.BlacklistDuration,
		"until", provider.BlacklistedUntil)
}

func (fm *FailoverManager) countActiveProviders() int {
	count := 0
	now := time.Now()

	for _, provider := range fm.providers {
		if provider.State != StateBlacklisted || now.After(provider.BlacklistedUntil) {
			count++
		}
	}

	return count
}

// Configuration methods
func (fm *FailoverManager) SetBlacklistMode(enable bool) {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()
	fm.config.EnableBlacklisting = enable
}

func (fm *FailoverManager) SetMinActiveProviders(min int) {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()
	fm.config.MinActiveProviders = min
}

func (fm *FailoverManager) SetErrorThreshold(threshold int) {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()
	fm.config.ErrorThreshold = threshold
}

// GetProviderStatus returns status of all providers
func (fm *FailoverManager) GetProviderStatus() []*Provider {
	fm.mutex.RLock()
	defer fm.mutex.RUnlock()

	status := make([]*Provider, len(fm.providers))
	copy(status, fm.providers)
	return status
}

// Cleanup closes all provider connections
func (fm *FailoverManager) Cleanup() {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	for _, provider := range fm.providers {
		if provider.Client != nil {
			provider.Client.Close()
		}
	}
}
