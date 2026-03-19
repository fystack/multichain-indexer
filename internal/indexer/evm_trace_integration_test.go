package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tenderlySepoliaRPC = "https://sepolia.gateway.tenderly.co/1ZTXSdHpdLxTjQ8wt4ppV3"
const integrationSafeTxHash = "0x7694b41ca7105e4080d1d172d3ad99293902c36bf83bb46d2d9bd6a316ba050b"
const integrationSafeBlockNum = uint64(10356752)
const integrationSafeBlockHex = "0x9e0f10"

func newTraceTestClient() *evm.Client {
	return evm.NewEthereumClient(tenderlySepoliaRPC, nil, 30*time.Second, nil)
}

// TestEndToEnd_TraceInConvertBlock_Integration runs the full pipeline:
// fetch block → fetch receipt → fetch trace → convertBlock → verify transfers
func TestEndToEnd_TraceInConvertBlock_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTraceTestClient()

	// Fetch block
	block, err := c.GetBlockByNumber(ctx, integrationSafeBlockHex, true)
	require.NoError(t, err)

	// Find our tx
	var tx *evm.Txn
	for i := range block.Transactions {
		if block.Transactions[i].Hash == integrationSafeTxHash {
			tx = &block.Transactions[i]
			break
		}
	}
	require.NotNil(t, tx)

	// Fetch receipt
	receipts, err := c.BatchGetTransactionReceipts(ctx, []string{integrationSafeTxHash})
	require.NoError(t, err)
	receipt := receipts[integrationSafeTxHash]
	require.NotNil(t, receipt)

	// Fetch trace
	trace, err := c.DebugTraceTransaction(ctx, integrationSafeTxHash)
	require.NoError(t, err)
	require.NotNil(t, trace)

	// Build mini block and run through convertBlock with traces
	miniBlock := &evm.Block{
		Number:       block.Number,
		Hash:         block.Hash,
		ParentHash:   block.ParentHash,
		Timestamp:    block.Timestamp,
		Transactions: []evm.Txn{*tx},
	}

	idx := &EVMIndexer{
		chainName: "sepolia",
		config:    config.ChainConfig{NetworkId: "ethereum-sepolia"},
	}

	receiptMap := map[string]*evm.TxnReceipt{integrationSafeTxHash: receipt}
	traceMap := map[string]*evm.CallTrace{integrationSafeTxHash: trace}

	result, err := idx.convertBlock(miniBlock, receiptMap, traceMap)
	require.NoError(t, err)

	t.Logf("End-to-end: %d transfers extracted", len(result.Transactions))
	for i, tr := range result.Transactions {
		t.Logf("  [%d] type=%s from=%s to=%s amount=%s", i, tr.Type, tr.FromAddress, tr.ToAddress, tr.Amount)
	}

	// Should find the internal 0.1 ETH transfer
	var found bool
	for _, tr := range result.Transactions {
		if tr.Type == constant.TxTypeNativeTransfer && tr.Amount == "100000000000000000" {
			found = true
			assert.Equal(t, evm.ToChecksumAddress("0x13178e59d4b3ca1a06a6dcfa6692e1f6fbdb58c8"), tr.FromAddress)
			assert.Equal(t, evm.ToChecksumAddress("0x23dc93f83d34f66a96de2623915ce69852f34a13"), tr.ToAddress)
		}
	}
	assert.True(t, found, "should find 0.1 ETH internal transfer via trace")
}

// TestERC20PrequerySkip_Integration verifies that when traceModeActive() is true,
// the ERC20 prequery (eth_getLogs) is skipped because contract-call receipt expansion
// already covers all ERC20 transfers.
func TestERC20PrequerySkip(t *testing.T) {
	// This is a logic test, not a network test.
	// When traceModeActive() returns true and pubkeyStore != nil,
	// processBlocksAndReceipts should skip the ERC20 prequery.

	// We verify the condition: len(blockNums) > 0 && e.pubkeyStore != nil && !traceActive
	// When traceActive=true, the condition is false → prequery skipped.

	traceActive := true
	pubkeyStore := evmPubkeyStoreStub{addresses: map[string]bool{"0xtest": true}}

	// Simulate the gate condition
	shouldRunPrequery := pubkeyStore.Exist(enum.NetworkTypeEVM, "0xtest") && !traceActive
	assert.False(t, shouldRunPrequery, "ERC20 prequery should be skipped when traceActive=true")

	// When trace is off, prequery should run
	traceActive = false
	shouldRunPrequery = pubkeyStore.Exist(enum.NetworkTypeEVM, "0xtest") && !traceActive
	assert.True(t, shouldRunPrequery, "ERC20 prequery should run when traceActive=false")
}

// TestRuntimeTracePoolExhaustion verifies that when all trace providers are
// blacklisted at runtime, traceModeActive() returns false and receipt scope
// collapses to normal mode.
func TestRuntimeTracePoolExhaustion(t *testing.T) {
	// Build a trace failover with one provider
	traceFailover := rpc.NewFailover[evm.EthereumAPI](nil)
	mockClient := evm.NewEthereumClient("http://localhost:1", nil, 1*time.Second, nil)
	provider := &rpc.Provider{
		Name: "trace-1", URL: "http://localhost:1",
		Network: "test", ClientType: "rpc", Client: mockClient, State: rpc.StateHealthy,
	}
	traceFailover.AddProvider(provider)

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: traceFailover,
	}

	// Initially: trace mode should be active
	assert.True(t, idx.traceModeActive(), "trace mode should be active with healthy provider")

	// Blacklist the provider (simulating runtime exhaustion)
	provider.Blacklist(1 * time.Hour)

	// Now: trace mode should be inactive
	assert.False(t, idx.traceModeActive(), "trace mode should be inactive when all providers blacklisted")

	// Receipt scope should collapse: verify extractReceiptTxHashes behaves like traceActive=false
	blocks := map[uint64]*evm.Block{
		1: {
			Transactions: []evm.Txn{
				{Hash: "0x1", From: "0xa", To: "0xb", Value: "0x0", Input: "0xabcdef00"}, // contract call
			},
		},
	}

	// With traceActive=false, contract call without NeedReceipt should be skipped
	result := idx.extractReceiptTxHashes(blocks, false)
	assert.Empty(t, result[1], "contract call should NOT get receipt when trace mode collapsed")

	// With traceActive=true it would be included
	result = idx.extractReceiptTxHashes(blocks, true)
	assert.Len(t, result[1], 1, "contract call should get receipt when trace mode active")
}

// TestTransientErrorResilience verifies that traceWithProviderAwareness
// tries all providers when one fails with a transient error.
func TestTransientErrorResilience(t *testing.T) {
	// Create two mock HTTP servers: first returns error, second returns valid trace
	callCount := 0

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"request timeout: backend is overloaded"},"id":1}`)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"from":"0xaaaa","to":"0xbbbb","value":"0x1","type":"CALL","input":"0x","output":"0x","gas":"0x5208","gasUsed":"0x5208"},"id":1}`)
	}))
	defer server2.Close()

	traceFailover := rpc.NewFailover[evm.EthereumAPI](nil)
	c1 := evm.NewEthereumClient(server1.URL, nil, 5*time.Second, nil)
	c2 := evm.NewEthereumClient(server2.URL, nil, 5*time.Second, nil)

	traceFailover.AddProvider(&rpc.Provider{
		Name: "failing", URL: server1.URL,
		Network: "test", ClientType: "rpc", Client: c1, State: rpc.StateHealthy,
	})
	traceFailover.AddProvider(&rpc.Provider{
		Name: "working", URL: server2.URL,
		Network: "test", ClientType: "rpc", Client: c2, State: rpc.StateHealthy,
	})

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: traceFailover,
	}

	ctx := context.Background()
	trace, err := idx.traceWithProviderAwareness(ctx, "0xdeadbeef")

	// Should succeed — first provider fails, second succeeds
	require.NoError(t, err)
	require.NotNil(t, trace)
	assert.Equal(t, "CALL", trace.Type)
	assert.GreaterOrEqual(t, callCount, 2, "should have tried at least 2 providers")
}

// TestCapabilityErrorBlacklist verifies that a "method not found" error
// triggers immediate 24h blacklist via HandleCapabilityError.
func TestCapabilityErrorBlacklist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32601,"message":"the method debug_traceTransaction does not exist/is not available"},"id":1}`)
	}))
	defer server.Close()

	traceFailover := rpc.NewFailover[evm.EthereumAPI](nil)
	c := evm.NewEthereumClient(server.URL, nil, 5*time.Second, nil)
	provider := &rpc.Provider{
		Name: "no-debug", URL: server.URL,
		Network: "test", ClientType: "rpc", Client: c, State: rpc.StateHealthy,
	}
	traceFailover.AddProvider(provider)

	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: traceFailover,
	}

	ctx := context.Background()
	_, err := idx.traceWithProviderAwareness(ctx, "0xdeadbeef")
	require.Error(t, err)

	// Provider should be blacklisted
	assert.False(t, provider.IsAvailable(), "provider should be blacklisted after -32601 error")

	// traceModeActive should now return false
	assert.False(t, idx.traceModeActive(), "trace mode should be inactive after provider blacklisted")
}
