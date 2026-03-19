package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockTraceServer(response string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, response)
	}))
}

func newMockTraceFailover(serverURL string) *rpc.Failover[evm.EthereumAPI] {
	f := rpc.NewFailover[evm.EthereumAPI](nil)
	c := evm.NewEthereumClient(serverURL, nil, 5*time.Second, nil)
	f.AddProvider(&rpc.Provider{
		Name: "mock-trace", URL: serverURL,
		Network: "test", ClientType: "rpc", Client: c, State: rpc.StateHealthy,
	})
	return f
}

func TestFetchTraces_NilTraceFailover(t *testing.T) {
	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: nil,
	}

	blocks := map[uint64]*evm.Block{
		1: {Transactions: []evm.Txn{{Hash: "0x1", Input: "0xabcd"}}},
	}
	receipts := map[string]*evm.TxnReceipt{
		"0x1": {Status: "0x1"},
	}

	result := idx.fetchTraces(context.Background(), blocks, receipts)
	assert.Nil(t, result, "should return nil when traceFailover is nil")
}

func TestFetchTraces_NoCandidates(t *testing.T) {
	server := newMockTraceServer(`{"jsonrpc":"2.0","result":{},"id":1}`)
	defer server.Close()

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: newMockTraceFailover(server.URL),
	}

	// Only plain native transfers — no contract calls
	blocks := map[uint64]*evm.Block{
		1: {Transactions: []evm.Txn{
			{Hash: "0x1", Input: "", Value: "0x1", To: "0xbbbb"},    // plain transfer
			{Hash: "0x2", Input: "0x", Value: "0x1", To: "0xcccc"},  // plain transfer
		}},
	}
	receipts := map[string]*evm.TxnReceipt{
		"0x1": {Status: "0x1"},
		"0x2": {Status: "0x1"},
	}

	result := idx.fetchTraces(context.Background(), blocks, receipts)
	assert.Nil(t, result, "should return nil when no contract call candidates")
}

func TestFetchTraces_SkipsFailedReceipts(t *testing.T) {
	server := newMockTraceServer(`{"jsonrpc":"2.0","result":{"from":"0xa","to":"0xb","value":"0x0","type":"CALL","input":"0x"},"id":1}`)
	defer server.Close()

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: newMockTraceFailover(server.URL),
	}

	blocks := map[uint64]*evm.Block{
		1: {Transactions: []evm.Txn{
			{Hash: "0x1", Input: "0xabcd1234", To: "0xbbbb"}, // contract call, but receipt failed
			{Hash: "0x2", Input: "0xabcd1234", To: "0xcccc"}, // contract call, no receipt
		}},
	}
	receipts := map[string]*evm.TxnReceipt{
		"0x1": {Status: "0x0"}, // failed tx
		// "0x2" has no receipt
	}

	result := idx.fetchTraces(context.Background(), blocks, receipts)
	assert.Nil(t, result, "should skip txs with failed/nil receipts")
}

func TestFetchTraces_CollectsTracesFromMockRPC(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"from":"0xaaaa","to":"0xbbbb","value":"0x1","type":"CALL","input":"0x","output":"0x","gas":"0x5208","gasUsed":"0x5208"},"id":1}`)
	}))
	defer server.Close()

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: newMockTraceFailover(server.URL),
	}

	blocks := map[uint64]*evm.Block{
		1: {Transactions: []evm.Txn{
			{Hash: "0x1", Input: "0xabcd1234", To: "0xbbbb"},
			{Hash: "0x2", Input: "0xdeadbeef", To: "0xcccc"},
		}},
	}
	receipts := map[string]*evm.TxnReceipt{
		"0x1": {Status: "0x1"},
		"0x2": {Status: "0x1"},
	}

	result := idx.fetchTraces(context.Background(), blocks, receipts)
	require.NotNil(t, result)
	assert.Len(t, result, 2, "should have traces for both contract calls")
	assert.NotNil(t, result["0x1"])
	assert.NotNil(t, result["0x2"])
	assert.Equal(t, int32(2), callCount.Load(), "should make 2 RPC calls")
}

func TestFetchTraces_PartialFailure(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call fails
			fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"internal error"},"id":1}`)
			return
		}
		// Subsequent calls succeed
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"from":"0xaaaa","to":"0xbbbb","value":"0x1","type":"CALL","input":"0x","output":"0x","gas":"0x5208","gasUsed":"0x5208"},"id":1}`)
	}))
	defer server.Close()

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: newMockTraceFailover(server.URL),
	}

	blocks := map[uint64]*evm.Block{
		1: {Transactions: []evm.Txn{
			{Hash: "0x1", Input: "0xabcd1234", To: "0xaaaa"},
			{Hash: "0x2", Input: "0xdeadbeef", To: "0xbbbb"},
		}},
	}
	receipts := map[string]*evm.TxnReceipt{
		"0x1": {Status: "0x1"},
		"0x2": {Status: "0x1"},
	}

	result := idx.fetchTraces(context.Background(), blocks, receipts)
	// One tx fails, one succeeds — should still return partial results
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result), 1, "should have at least 1 trace despite partial failure")
}

func TestFetchTraces_RespectsConfigConcurrency(t *testing.T) {
	var maxConcurrent atomic.Int32
	var current atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := current.Add(1)
		// Track max concurrent
		for {
			old := maxConcurrent.Load()
			if c <= old || maxConcurrent.CompareAndSwap(old, c) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond) // simulate work
		current.Add(-1)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"from":"0xaaaa","to":"0xbbbb","value":"0x1","type":"CALL","input":"0x","output":"0x","gas":"0x5208","gasUsed":"0x5208"},"id":1}`)
	}))
	defer server.Close()

	idx := &EVMIndexer{
		config: config.ChainConfig{
			DebugTrace: true,
			TraceThrottle: config.TraceThrottle{
				Concurrency: 2, // limit to 2 concurrent
			},
		},
		traceFailover: newMockTraceFailover(server.URL),
	}

	// Create 6 contract call txs
	txs := make([]evm.Txn, 6)
	receipts := make(map[string]*evm.TxnReceipt)
	for i := range txs {
		hash := fmt.Sprintf("0x%d", i)
		txs[i] = evm.Txn{Hash: hash, Input: "0xabcd1234", To: "0xbbbb"}
		receipts[hash] = &evm.TxnReceipt{Status: "0x1"}
	}

	blocks := map[uint64]*evm.Block{1: {Transactions: txs}}
	result := idx.fetchTraces(context.Background(), blocks, receipts)

	require.NotNil(t, result)
	assert.Len(t, result, 6, "all 6 traces should be collected")
	assert.LessOrEqual(t, maxConcurrent.Load(), int32(2), "max concurrency should be <= 2")
}
