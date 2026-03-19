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
func (m *mockNetworkClient) GetURL() string          { return "http://mock" }
func (m *mockNetworkClient) Close() error            { return nil }

func newTestFailover() (*Failover[NetworkClient], *Provider) {
	f := NewFailover[NetworkClient](nil)
	p := &Provider{
		Name:       "test-provider",
		URL:        "http://mock",
		Network:    "test",
		ClientType: "rpc",
		Client:     &mockNetworkClient{},
		State:      StateHealthy,
	}
	f.AddProvider(p)
	return f, p
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

func TestAnalyzeAndHandleError_ConnectionError(t *testing.T) {
	f, p := newTestFailover()

	err := fmt.Errorf("connection refused")
	f.AnalyzeAndHandleError(p, err, 100*time.Millisecond)

	assert.False(t, p.IsAvailable(), "provider should be blacklisted after connection error")

	errorsByType := f.GetMetrics()["errors_by_type"].(map[string]int64)
	assert.Equal(t, int64(1), errorsByType["connection_error"])
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
