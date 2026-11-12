package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockNetworkClient satisfies NetworkClient for Failover[T] type constraint.
type mockNetworkClient struct{}

func (m *mockNetworkClient) CallRPC(_ context.Context, _ string, _ any) (*RPCResponse, error) {
	return nil, nil
}
func (m *mockNetworkClient) Do(_ context.Context, _, _ string, _ any, _ map[string]string) ([]byte, error) {
	return nil, nil
}
func (m *mockNetworkClient) GetNetworkType() string { return "evm" }
func (m *mockNetworkClient) GetClientType() string  { return "rpc" }
func (m *mockNetworkClient) GetURL() string         { return "http://mock" }
func (m *mockNetworkClient) Close() error           { return nil }

func newTestProvider(name string) *Provider {
	return &Provider{
		Name:       name,
		URL:        "http://mock/" + name,
		Network:    "test",
		ClientType: "rpc",
		Client:     &mockNetworkClient{},
		State:      StateHealthy,
	}
}

func newTestFailover() (*Failover[NetworkClient], *Provider) {
	f := NewFailover[NetworkClient](nil)
	p := newTestProvider("test-provider")
	f.AddProvider(p)
	return f, p
}

func TestDefaultFailoverConfig_ForceRotateThreshold(t *testing.T) {
	cfg := DefaultFailoverConfig()

	assert.Equal(t, 3, cfg.ForceRotateThreshold)
}

func TestGetBestProvider_ForceRotateSkipsCurrentProvider(t *testing.T) {
	for _, state := range []string{StateHealthy, StateDegraded, StateUnhealthy} {
		t.Run(state, func(t *testing.T) {
			cfg := DefaultFailoverConfig()
			cfg.ForceRotateThreshold = 3
			f := NewFailover[NetworkClient](&cfg)

			current := newTestProvider("current")
			current.State = state
			current.ConsecutiveErrors = cfg.ForceRotateThreshold
			next := newTestProvider("next")

			require.NoError(t, f.AddProvider(current))
			require.NoError(t, f.AddProvider(next))

			got, err := f.GetBestProvider()

			require.NoError(t, err)
			assert.Equal(t, next.Name, got.Name)

			metrics := f.GetMetrics()
			assert.Equal(t, int64(1), metrics["provider_switches"])
		})
	}
}

func TestAnalyzeAndHandleError_Timeout(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("request timeout: deadline exceeded")
	f.AnalyzeAndHandleError(p, err, 5*time.Second)

	// Provider should be blacklisted (timeout → 3min cooldown)
	assert.False(t, p.IsAvailable(), "provider should be blacklisted after timeout error")
	assert.Equal(t, StateBlacklisted, p.State)

	metrics := f.GetMetrics()
	assert.Equal(t, int64(1), metrics["total_requests"])
	assert.Equal(t, int64(1), metrics["failed_requests"])
	errorsByType := metrics["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["timeout"])
}

func TestAnalyzeAndHandleError_RateLimit(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("429 too many requests")
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	assert.False(t, p.IsAvailable(), "provider should be blacklisted after rate limit")
	assert.Equal(t, StateBlacklisted, p.State)

	errorsByType := f.GetMetrics()["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["rate_limit"])
}

func TestAnalyzeAndHandleError_RestrictedQuery(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("rpc error: RPC error -32701: Please specify an address in your request or, to remove restrictions, order a dedicated full node here: https://www.allnodes.com/eth/host")
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	assert.False(t, p.IsAvailable(), "provider should be blacklisted after restricted query error")
	assert.Equal(t, StateBlacklisted, p.State)
	assert.True(t, time.Now().Add(4*time.Minute).Before(p.BlacklistedUntil))

	errorsByType := f.GetMetrics()["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["restricted_query"])
}

func TestAnalyzeAndHandleError_ConnectionError(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("connection refused")
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	assert.False(t, p.IsAvailable(), "provider should be blacklisted after connection error")

	errorsByType := f.GetMetrics()["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["connection_error"])
}

func TestAnalyzeAndHandleError_TLSError(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("eth_blockNumber failed: remote error: tls: internal error")
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	assert.False(t, p.IsAvailable(), "provider should be blacklisted after TLS error")

	errorsByType := f.GetMetrics()["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["tls_error"])
}

func TestAnalyzeAndHandleError_GenericError(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("some random error")
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	// Generic errors call Fail() → degraded/unhealthy, NOT blacklisted
	assert.True(t, p.IsAvailable(), "provider should still be available after generic error")
	assert.NotEqual(t, StateBlacklisted, p.State)

	metrics := f.GetMetrics()
	assert.Equal(t, int64(1), metrics["total_requests"])
	assert.Equal(t, int64(1), metrics["failed_requests"])
	errorsByType := metrics["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["generic_error"])
	assert.Equal(t, int64(1), errorsByType["unknown_error_type"])
}

func TestRecordSuccess(t *testing.T) {
	f, p := newTestFailover()

	// Make the provider degraded (need 2+ consecutive errors)
	p.Fail(&f.config)
	p.Fail(&f.config)
	require.Equal(t, StateDegraded, p.State)

	f.RecordSuccess(p, 100*time.Millisecond)

	assert.Equal(t, StateHealthy, p.State)
	assert.Equal(t, 0, p.ConsecutiveErrors)

	metrics := f.GetMetrics()
	assert.Equal(t, int64(1), metrics["total_requests"])
	assert.Equal(t, int64(1), metrics["successful_requests"])
	providerCounts := metrics["provider_request_counts"].(map[string]int64)
	assert.Equal(t, int64(1), providerCounts["test-provider"])
}

func TestHandleCapabilityError(t *testing.T) {
	f, p := newTestFailover()

	f.HandleCapabilityError(p, 50*time.Millisecond, 24*time.Hour)

	assert.False(t, p.IsAvailable(), "provider should be blacklisted")
	assert.Equal(t, StateBlacklisted, p.State)

	metrics := f.GetMetrics()
	assert.Equal(t, int64(1), metrics["total_requests"])
	assert.Equal(t, int64(1), metrics["failed_requests"])
	assert.Equal(t, int64(1), metrics["blacklist_events"])
	errorsByType := metrics["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["capability_error"])
}

func TestExecuteCore_GenericErrorsForceRotateToHealthySibling(t *testing.T) {
	cfg := DefaultFailoverConfig()
	cfg.ForceRotateThreshold = 3
	f := NewFailover[NetworkClient](&cfg)

	first := newTestProvider("first")
	second := newTestProvider("second")
	require.NoError(t, f.AddProvider(first))
	require.NoError(t, f.AddProvider(second))

	for i := 0; i < cfg.ForceRotateThreshold; i++ {
		provider, err := f.GetBestProvider()
		require.NoError(t, err)
		require.Equal(t, first.Name, provider.Name)

		err = f.executeCore(context.Background(), provider, func(NetworkClient) error {
			return fmt.Errorf("unclassified backend failure")
		})
		require.Error(t, err)
	}

	got, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, second.Name, got.Name)

	metrics := f.GetMetrics()
	errorsByType := metrics["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(cfg.ForceRotateThreshold), errorsByType["generic_error"])
	assert.Equal(t, int64(cfg.ForceRotateThreshold), errorsByType["unknown_error_type"])
	assert.Equal(t, int64(1), metrics["provider_switches"])
}

func TestExecuteCore_TransientGenericErrorsDoNotForceRotate(t *testing.T) {
	cfg := DefaultFailoverConfig()
	cfg.ForceRotateThreshold = 3
	f := NewFailover[NetworkClient](&cfg)

	first := newTestProvider("first")
	second := newTestProvider("second")
	require.NoError(t, f.AddProvider(first))
	require.NoError(t, f.AddProvider(second))

	for i := 0; i < cfg.ForceRotateThreshold-1; i++ {
		provider, err := f.GetBestProvider()
		require.NoError(t, err)
		require.Equal(t, first.Name, provider.Name)

		err = f.executeCore(context.Background(), provider, func(NetworkClient) error {
			return fmt.Errorf("transient unclassified backend failure")
		})
		require.Error(t, err)
	}

	provider, err := f.GetBestProvider()
	require.NoError(t, err)
	require.Equal(t, first.Name, provider.Name)

	err = f.executeCore(context.Background(), provider, func(NetworkClient) error {
		return nil
	})
	require.NoError(t, err)

	got, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, first.Name, got.Name)
	assert.Equal(t, 0, first.ConsecutiveErrors)

	metrics := f.GetMetrics()
	assert.Equal(t, int64(0), metrics["provider_switches"])
}

func newBackupFailover(t *testing.T) (*Failover[NetworkClient], *Provider, *Provider) {
	t.Helper()
	f := NewFailover[NetworkClient](nil)
	primary := newTestProvider("primary")
	backup := newTestProvider("backup")
	backup.Backup = true
	require.NoError(t, f.AddProvider(backup))
	require.NoError(t, f.AddProvider(primary))
	return f, primary, backup
}

func TestGetBestProvider_PrefersPrimaryOverBackup(t *testing.T) {
	f, primary, _ := newBackupFailover(t)

	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, primary.Name, p.Name)
}

func TestGetBestProvider_ReturnsToPrimaryAfterBlacklistExpires(t *testing.T) {
	f, primary, backup := newBackupFailover(t)

	primary.Blacklist(time.Hour)
	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, backup.Name, p.Name)

	primary.Blacklist(-time.Second)
	p, err = f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, primary.Name, p.Name)

	p, err = f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, primary.Name, p.Name)
}

func TestGetAvailableProviders_ExcludesBackupWhilePrimaryAvailable(t *testing.T) {
	f, primary, backup := newBackupFailover(t)

	got := f.GetAvailableProviders()
	require.Len(t, got, 1)
	assert.Equal(t, primary.Name, got[0].Name)

	primary.Blacklist(time.Hour)
	got = f.GetAvailableProviders()
	require.Len(t, got, 1)
	assert.Equal(t, backup.Name, got[0].Name)
}

func TestExecuteWithRetryProvider_FallsBackToBackup(t *testing.T) {
	f, primary, backup := newBackupFailover(t)

	f.AnalyzeAndHandleError(primary, fmt.Errorf("context deadline exceeded"), time.Second)
	require.Equal(t, StateBlacklisted, primary.State)

	calls := 0
	err := f.ExecuteWithRetryProvider(context.Background(), primary, func(NetworkClient) error {
		calls++
		return nil
	}, true)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, StateBlacklisted, primary.State)
	assert.Equal(t, StateHealthy, backup.State)
	assert.Equal(t, int64(1), f.GetMetrics()["provider_request_counts"].(map[string]int64)[backup.Name])
}

func TestGetBestProvider_ForceRotateReachesBackup(t *testing.T) {
	f, primary, backup := newBackupFailover(t)

	for i := 0; i < f.config.ForceRotateThreshold; i++ {
		p, err := f.GetBestProvider()
		require.NoError(t, err)
		require.Equal(t, primary.Name, p.Name)
		require.Error(t, f.executeCore(context.Background(), p, func(NetworkClient) error {
			return fmt.Errorf("unclassified backend failure")
		}))
	}

	for i := 0; i < 2; i++ {
		p, err := f.GetBestProvider()
		require.NoError(t, err)
		assert.Equal(t, backup.Name, p.Name)
	}
	assert.False(t, primary.IsAvailable())
	assert.Equal(t, int64(2), f.GetMetrics()["provider_switches"])

	expireBlacklist(primary)
	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, primary.Name, p.Name)
	assert.Equal(t, 0, primary.ConsecutiveErrors)
}

func TestEmergencyRecovery_TotalOutageDoesNotEscalate(t *testing.T) {
	f, primary, backup := newBackupFailover(t)
	timeout := fmt.Errorf("context deadline exceeded")

	for i := 0; i < 10; i++ {
		p, err := f.GetBestProvider()
		require.NoError(t, err)
		f.AnalyzeAndHandleError(p, timeout, time.Second)
	}

	for _, p := range []*Provider{primary, backup} {
		assert.Equal(t, 1, p.BlacklistStrikes, p.Name)
		assert.True(t, p.BlacklistedUntil.Before(time.Now().Add(4*time.Minute)), p.Name)
	}
}

type namedClient struct {
	mockNetworkClient
	name string
}

func expireBlacklist(p *Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.BlacklistedUntil = time.Now().Add(-time.Second)
}

func probeRequired(p *Provider) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.probeRequired
}

func newProbedFailover(t *testing.T, heads map[string]uint64, failing map[string]bool) (*Failover[NetworkClient], *Provider, *Provider) {
	t.Helper()
	f, primary, backup := newBackupFailover(t)
	primary.Client = &namedClient{name: "primary"}
	backup.Client = &namedClient{name: "backup"}
	f.SetProbe(func(_ context.Context, c NetworkClient) (uint64, error) {
		name := c.(*namedClient).name
		if failing[name] {
			return 0, fmt.Errorf("context deadline exceeded")
		}
		return heads[name], nil
	}, 20)
	return f, primary, backup
}

func TestBlacklistEscalating(t *testing.T) {
	p := newTestProvider("p")
	maxD := 30 * time.Minute

	for _, want := range []time.Duration{3, 6, 12, 24, 30} {
		assert.Equal(t, want*time.Minute, p.BlacklistEscalating(3*time.Minute, maxD, false))
		expireBlacklist(p)
	}

	p.Success(time.Millisecond)
	assert.Equal(t, 3*time.Minute, p.BlacklistEscalating(3*time.Minute, maxD, false))

	q := newTestProvider("q")
	assert.Equal(t, 24*time.Hour, q.BlacklistEscalating(24*time.Hour, maxD, false))
	expireBlacklist(q)
	assert.Equal(t, 24*time.Hour, q.BlacklistEscalating(24*time.Hour, maxD, false))
}

func TestBlacklistEscalating_ConcurrentFailuresAreOneStrike(t *testing.T) {
	p := newTestProvider("p")
	maxD := 30 * time.Minute

	for i := 0; i < 5; i++ {
		p.BlacklistEscalating(3*time.Minute, maxD, false)
	}
	assert.Equal(t, 1, p.BlacklistStrikes)
	assert.True(t, p.BlacklistedUntil.Before(time.Now().Add(4*time.Minute)))

	p.BlacklistEscalating(24*time.Hour, maxD, false)
	assert.Equal(t, 1, p.BlacklistStrikes)
	assert.True(t, p.BlacklistedUntil.After(time.Now().Add(23*time.Hour)))
}

func TestFinishProbe_DropsResultWhenStateChanged(t *testing.T) {
	maxD := 30 * time.Minute

	recovered := newTestProvider("recovered")
	recovered.BlacklistEscalating(3*time.Minute, maxD, true)
	expireBlacklist(recovered)
	action, gen := recovered.settleExpiredBlacklist(true)
	require.Equal(t, expiredBlacklistNeedsProbe, action)
	recovered.emergencyRecover()

	applied, _ := recovered.finishProbe(gen, false, maxD)
	assert.False(t, applied)
	assert.True(t, recovered.IsAvailable())

	reblacklisted := newTestProvider("reblacklisted")
	reblacklisted.BlacklistEscalating(3*time.Minute, maxD, true)
	expireBlacklist(reblacklisted)
	action, gen = reblacklisted.settleExpiredBlacklist(true)
	require.Equal(t, expiredBlacklistNeedsProbe, action)
	reblacklisted.emergencyRecover()
	reblacklisted.BlacklistEscalating(3*time.Minute, maxD, true)

	applied, _ = reblacklisted.finishProbe(gen, true, maxD)
	assert.False(t, applied)
	assert.False(t, reblacklisted.IsAvailable())

	action, _ = reblacklisted.settleExpiredBlacklist(true)
	assert.Equal(t, expiredBlacklistUnchanged, action)
}

func TestProbe_FailingPrimaryStaysBlacklistedWithLongerCooldown(t *testing.T) {
	failing := map[string]bool{"primary": true}
	f, primary, backup := newProbedFailover(t, map[string]uint64{"primary": 100, "backup": 100}, failing)

	f.AnalyzeAndHandleError(primary, fmt.Errorf("context deadline exceeded"), time.Second)
	expireBlacklist(primary)

	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, backup.Name, p.Name)

	assert.Eventually(t, func() bool {
		primary.mu.RLock()
		defer primary.mu.RUnlock()
		return !primary.probing && primary.BlacklistStrikes == 2
	}, time.Second, 5*time.Millisecond)
	assert.True(t, probeRequired(primary))
	assert.True(t, time.Now().Add(5*time.Minute).Before(primary.BlacklistedUntil))

	p, err = f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, backup.Name, p.Name)
}

func TestProbe_PassingPrimaryRecoversAndTakesTrafficBack(t *testing.T) {
	f, primary, backup := newProbedFailover(t, map[string]uint64{"primary": 100, "backup": 105}, nil)

	f.AnalyzeAndHandleError(primary, fmt.Errorf("context deadline exceeded"), time.Second)
	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, backup.Name, p.Name)

	expireBlacklist(primary)
	assert.Eventually(t, func() bool {
		p, err := f.GetBestProvider()
		return err == nil && p.Name == primary.Name
	}, time.Second, 5*time.Millisecond)
}

func TestProbe_LaggingPrimaryStaysBlacklisted(t *testing.T) {
	f, primary, backup := newProbedFailover(t, map[string]uint64{"primary": 100, "backup": 200}, nil)

	f.AnalyzeAndHandleError(primary, fmt.Errorf("context deadline exceeded"), time.Second)
	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, backup.Name, p.Name)

	expireBlacklist(primary)
	_, err = f.GetBestProvider()
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		primary.mu.RLock()
		defer primary.mu.RUnlock()
		return !primary.probing && primary.BlacklistStrikes == 2
	}, time.Second, 5*time.Millisecond)
	assert.False(t, primary.IsAvailable())
}

func TestProbe_PlainBlacklistRecoversOnTimer(t *testing.T) {
	f, primary, _ := newProbedFailover(t, nil, map[string]bool{"primary": true})

	f.HandleCapabilityError(primary, time.Millisecond, time.Hour)
	expireBlacklist(primary)

	p, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.Equal(t, primary.Name, p.Name)
}
