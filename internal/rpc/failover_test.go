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

func TestAnalyzeAndHandleError_ChainUnavailable(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf(`getBlock failed: HTTP 400 Bad Request: {"error":{"message":"chain is not available on free plan, please upgrade to paid plan","code":35}}`)
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	assert.False(t, p.IsAvailable(), "a node that doesn't serve the chain should be blacklisted immediately")
	assert.Equal(t, StateBlacklisted, p.State)
	// Long cooldown (24h) — the condition is permanent for that node.
	assert.True(t, time.Now().Add(23*time.Hour).Before(p.BlacklistedUntil))

	errorsByType := f.GetMetrics()["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["chain_unavailable"])
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

func TestEmergencyRecovery_SpacedByInterval(t *testing.T) {
	cfg := DefaultFailoverConfig()
	cfg.EmergencyRecoveryInterval = 100 * time.Millisecond
	f := NewFailover[NetworkClient](&cfg)

	a := newTestProvider("a")
	b := newTestProvider("b")
	require.NoError(t, f.AddProvider(a))
	require.NoError(t, f.AddProvider(b))

	// Whole pool down (long blacklist so it doesn't expire mid-test).
	a.Blacklist(time.Hour)
	b.Blacklist(time.Hour)

	// First call recovers one provider.
	p, err := f.GetBestProvider()
	require.NoError(t, err)
	require.NotNil(t, p)

	// Knock the recovered one back out so the pool is fully down again.
	p.Blacklist(time.Hour)

	// Immediately: within the interval -> caller is told to back off, no churn.
	_, err = f.GetBestProvider()
	require.ErrorIs(t, err, errAllProvidersBackoff)

	// After the interval passes, emergency recovery is allowed again.
	time.Sleep(120 * time.Millisecond)
	p2, err := f.GetBestProvider()
	require.NoError(t, err)
	require.NotNil(t, p2)
}

func TestExecuteCore_SlowSuccessBlacklistsProvider(t *testing.T) {
	cfg := DefaultFailoverConfig()
	cfg.SlowResponseThreshold = 10 * time.Millisecond
	cfg.SlowResponseCooldown = time.Minute
	cfg.MinActiveProviders = 2
	f := NewFailover[NetworkClient](&cfg)

	// Three providers so blacklisting one still leaves >= MinActiveProviders.
	first := newTestProvider("first")
	require.NoError(t, f.AddProvider(first))
	require.NoError(t, f.AddProvider(newTestProvider("second")))
	require.NoError(t, f.AddProvider(newTestProvider("third")))

	err := f.executeCore(context.Background(), first, func(NetworkClient) error {
		time.Sleep(30 * time.Millisecond) // exceeds SlowResponseThreshold
		return nil
	})
	require.NoError(t, err)

	assert.False(t, first.IsAvailable(), "slow-but-successful provider should be blacklisted")

	metrics := f.GetMetrics()
	assert.Equal(t, int64(1), metrics["blacklist_events"])
	errorsByType := metrics["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["slow_response"])

	got, err := f.GetBestProvider()
	require.NoError(t, err)
	assert.NotEqual(t, first.Name, got.Name, "should rotate off the slow provider")
}

func TestExecuteCore_SlowSuccessKeepsProviderWhenPoolAtMinimum(t *testing.T) {
	cfg := DefaultFailoverConfig()
	cfg.SlowResponseThreshold = 10 * time.Millisecond
	cfg.SlowResponseCooldown = time.Minute
	cfg.MinActiveProviders = 2
	f := NewFailover[NetworkClient](&cfg)

	// Only MinActiveProviders providers: blacklisting would starve the pool.
	first := newTestProvider("first")
	require.NoError(t, f.AddProvider(first))
	require.NoError(t, f.AddProvider(newTestProvider("second")))

	err := f.executeCore(context.Background(), first, func(NetworkClient) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	})
	require.NoError(t, err)

	assert.True(t, first.IsAvailable(), "must keep slow provider to preserve minimum active pool")
	assert.Equal(t, int64(0), f.GetMetrics()["blacklist_events"])
}

func TestExecuteCore_FastSuccessDoesNotBlacklist(t *testing.T) {
	cfg := DefaultFailoverConfig()
	cfg.SlowResponseThreshold = 500 * time.Millisecond
	f := NewFailover[NetworkClient](&cfg)

	first := newTestProvider("first")
	require.NoError(t, f.AddProvider(first))
	require.NoError(t, f.AddProvider(newTestProvider("second")))
	require.NoError(t, f.AddProvider(newTestProvider("third")))

	err := f.executeCore(context.Background(), first, func(NetworkClient) error {
		return nil // fast
	})
	require.NoError(t, err)

	assert.True(t, first.IsAvailable())
	assert.Equal(t, int64(0), f.GetMetrics()["blacklist_events"])
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
