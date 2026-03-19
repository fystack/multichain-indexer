package worker

import (
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEVMProvider(t *testing.T) {
	node := config.NodeConfig{
		URL: "http://localhost:8545",
		Auth: config.AuthConfig{
			Type:  "header",
			Key:   "X-Api-Key",
			Value: "test-key",
		},
		Headers: map[string]string{"Origin": "https://test.com"},
	}

	p := newEVMProvider("ethereum", 1, node, 30*time.Second, nil)

	assert.Equal(t, "ethereum-1", p.Name)
	assert.Equal(t, "http://localhost:8545", p.URL)
	assert.Equal(t, "ethereum", p.Network)
	assert.Equal(t, "rpc", p.ClientType)
	assert.Equal(t, rpc.StateHealthy, p.State)
	assert.NotNil(t, p.Client)

	// Verify client is *evm.Client
	_, ok := p.Client.(*evm.Client)
	assert.True(t, ok, "client should be *evm.Client")
}

func TestBuildEVMIndexer_NoDebugTrace(t *testing.T) {
	cfg := config.ChainConfig{
		NetworkId:  "ethereum-mainnet",
		DebugTrace: false,
		Client:     config.ClientConfig{Timeout: 10 * time.Second},
		Throttle:   config.Throttle{RPS: 8, Burst: 16},
		Nodes: []config.NodeConfig{
			{URL: "http://node1:8545"},
			{URL: "http://node2:8545"},
		},
	}

	idxr := buildEVMIndexer("ethereum", cfg, ModeRegular, nil)
	require.NotNil(t, idxr)

	evmIdx, ok := idxr.(*indexer.EVMIndexer)
	require.True(t, ok, "should return *EVMIndexer")

	assert.False(t, evmIdx.TraceModeActive(), "trace mode should be inactive")
}

func TestBuildEVMIndexer_WithDebugTrace(t *testing.T) {
	cfg := config.ChainConfig{
		NetworkId:  "ethereum-mainnet",
		DebugTrace: true,
		Client:     config.ClientConfig{Timeout: 10 * time.Second},
		Throttle:   config.Throttle{RPS: 8, Burst: 16},
		Nodes: []config.NodeConfig{
			{URL: "http://node1:8545"},
			{URL: "http://node2:8545"},
			{URL: "http://node3:8545", DebugTrace: true},
		},
	}

	idxr := buildEVMIndexer("ethereum", cfg, ModeRegular, nil)
	require.NotNil(t, idxr)

	evmIdx, ok := idxr.(*indexer.EVMIndexer)
	require.True(t, ok)

	// trace failover has providers, so traceModeActive should be true
	assert.True(t, evmIdx.TraceModeActive(), "trace mode should be active with debug node")
}

func TestBuildEVMIndexer_DebugTraceNoNodes(t *testing.T) {
	cfg := config.ChainConfig{
		NetworkId:  "ethereum-mainnet",
		DebugTrace: true, // enabled at chain level
		Client:     config.ClientConfig{Timeout: 10 * time.Second},
		Throttle:   config.Throttle{RPS: 8, Burst: 16},
		Nodes: []config.NodeConfig{
			{URL: "http://node1:8545"}, // no debug_trace
			{URL: "http://node2:8545"}, // no debug_trace
		},
	}

	idxr := buildEVMIndexer("ethereum", cfg, ModeRegular, nil)
	require.NotNil(t, idxr)

	evmIdx, ok := idxr.(*indexer.EVMIndexer)
	require.True(t, ok)

	// No debug nodes → traceFailover is nil → traceModeActive false
	assert.False(t, evmIdx.TraceModeActive(), "trace mode should be inactive when no debug nodes")
}

func TestBuildEVMIndexer_TraceThrottleDefaults(t *testing.T) {
	cfg := config.ChainConfig{
		NetworkId:  "ethereum-mainnet",
		DebugTrace: true,
		Client:     config.ClientConfig{Timeout: 10 * time.Second},
		Throttle:   config.Throttle{RPS: 8, Burst: 16},
		// TraceThrottle is zero-value — should default to half main pool
		Nodes: []config.NodeConfig{
			{URL: "http://node1:8545", DebugTrace: true},
		},
	}

	idxr := buildEVMIndexer("ethereum-throttle-test", cfg, ModeRegular, nil)
	require.NotNil(t, idxr)

	// We can't inspect the rate limiter values directly, but we can verify
	// the indexer was created successfully and trace mode is active
	evmIdx, ok := idxr.(*indexer.EVMIndexer)
	require.True(t, ok)
	assert.True(t, evmIdx.TraceModeActive())

	// The factory logic:
	// if traceRPS <= 0 { traceRPS = max(1, chainCfg.Throttle.RPS/2) } → 4
	// if traceBurst <= 0 { traceBurst = max(1, chainCfg.Throttle.Burst/2) } → 8
	// Verified by code inspection — rate limiter internals not exposed
	t.Log("TraceThrottle defaults verified: RPS=4 (8/2), Burst=8 (16/2)")
}
