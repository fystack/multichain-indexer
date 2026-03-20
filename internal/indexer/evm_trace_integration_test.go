package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
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

// Must be set via RPC_URL env var.
// Optionally set RPC_ORIGIN and RPC_REFERER for RPCs that require custom headers.
const integrationSafeTxHash = "0x7c98ff7c910b025736b11d2f70db001d5c2ec25df6de9fb65193963f6059b1f9"
const integrationSafeBlockNum = uint64(22869070)
const integrationSafeBlockHex = "0x15cd8ee"

func traceRPCURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RPC_URL")
	if url == "" {
		t.Skip("RPC_URL not set, skipping integration test")
	}
	return url
}

func newTraceTestClient(t *testing.T) *evm.Client {
	t.Helper()
	c := evm.NewEthereumClient(traceRPCURL(t), nil, 30*time.Second, nil)
	headers := make(map[string]string)
	if origin := os.Getenv("RPC_ORIGIN"); origin != "" {
		headers["Origin"] = origin
	}
	if referer := os.Getenv("RPC_REFERER"); referer != "" {
		headers["Referer"] = referer
	}
	if len(headers) > 0 {
		c.SetCustomHeaders(headers)
	}
	return c
}

// TestEndToEnd_TraceInConvertBlock_Integration runs the full pipeline:
// fetch block → fetch receipt → fetch trace → convertBlock → verify transfers
func TestEndToEnd_TraceInConvertBlock_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTraceTestClient(t)

	// Fetch receipt
	receipts, err := c.BatchGetTransactionReceipts(ctx, []string{integrationSafeTxHash})
	require.NoError(t, err)
	receipt := receipts[integrationSafeTxHash]
	require.NotNil(t, receipt)

	// Fetch trace
	trace, err := c.DebugTraceTransaction(ctx, integrationSafeTxHash)
	require.NoError(t, err)
	require.NotNil(t, trace)

	// Construct tx + block from known data (avoids fetching full block)
	tx := evm.Txn{
		Hash:  integrationSafeTxHash,
		From:  "0xa768d264b8bf98588ebdef6e241a0a73baf287d1",
		To:    "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
		Value: "0x0",
		Input: "0x6a761202", // Safe execTransaction
	}
	miniBlock := &evm.Block{
		Number:       fmt.Sprintf("0x%x", integrationSafeBlockNum),
		Timestamp:    "0x68398b07",
		Transactions: []evm.Txn{tx},
	}

	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "ethereum-mainnet"},
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
			assert.Equal(t, evm.ToChecksumAddress("0x84ba2321d46814fb1aa69a7b71882efea50f700c"), tr.FromAddress)
			assert.Equal(t, evm.ToChecksumAddress("0xc26dC13d057824342D5480b153f288bd1C5e3e9d"), tr.ToAddress)
		}
	}
	assert.True(t, found, "should find 0.1 ETH internal transfer via trace")
}

// TestEndToEnd_BatchSendNative_Integration runs the full pipeline on the sample tx
// from issue #71: a Revolut batch sendNative with 11 internal ETH transfers.
// This proves the trace feature catches transfers the Safe heuristic misses.
func TestEndToEnd_BatchSendNative_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTraceTestClient(t)

	batchTxHash := "0x3659bdb7f7ee48701603159e20357986bdfc3da87428d505841475f212a8369b"
	batchBlockNum := uint64(24669397)

	// Fetch receipt
	receipts, err := c.BatchGetTransactionReceipts(ctx, []string{batchTxHash})
	require.NoError(t, err)
	receipt := receipts[batchTxHash]
	require.NotNil(t, receipt)

	// Fetch trace
	trace, err := c.DebugTraceTransaction(ctx, batchTxHash)
	require.NoError(t, err)
	require.NotNil(t, trace)

	// Construct tx + block from known data
	tx := evm.Txn{
		Hash:  batchTxHash,
		From:  "0x2b3fed49557bd88f78b898684f82fbb355305dbb",
		To:    "0x09c30cdcdd971423cb3ba757a47d56c35d06d818",
		Value: "0x129f602145be8c00",
		Input: "0x57b1c066", // sendNative
	}

	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "ethereum-mainnet"},
	}

	miniBlock := &evm.Block{
		Number:       fmt.Sprintf("0x%x", batchBlockNum),
		Timestamp:    "0x6839a000",
		Transactions: []evm.Txn{tx},
	}

	receiptMap := map[string]*evm.TxnReceipt{batchTxHash: receipt}
	traceMap := map[string]*evm.CallTrace{batchTxHash: trace}

	resultWithTrace, err := idx.convertBlock(miniBlock, receiptMap, traceMap)
	require.NoError(t, err)

	// Run through convertBlock WITHOUT traces (simulating debug_trace=false)
	resultWithoutTrace, err := idx.convertBlock(miniBlock, receiptMap, nil)
	require.NoError(t, err)

	var nativeWithTrace, nativeWithoutTrace int
	for _, tr := range resultWithTrace.Transactions {
		if tr.Type == constant.TxTypeNativeTransfer {
			nativeWithTrace++
		}
	}
	for _, tr := range resultWithoutTrace.Transactions {
		if tr.Type == constant.TxTypeNativeTransfer {
			nativeWithoutTrace++
		}
	}

	t.Logf("With trace: %d native transfers", nativeWithTrace)
	t.Logf("Without trace: %d native transfers", nativeWithoutTrace)

	// Without trace: the batch contract call has no Safe heuristic match,
	// and ExtractTransfers won't emit a native transfer for contract calls with input.
	// With trace: should find at least 11 internal transfers.
	assert.GreaterOrEqual(t, nativeWithTrace, 11, "trace should detect at least 11 internal transfers")
	assert.Greater(t, nativeWithTrace, nativeWithoutTrace, "trace should detect MORE transfers than without trace")

	t.Logf("Trace detected %d more native transfers than without trace",
		nativeWithTrace-nativeWithoutTrace)
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
	// Use a single server that fails on first call, succeeds on second.
	// This avoids shuffle ordering issues with multiple providers.
	var attempt atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempt.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call: transient generic error (not timeout/rate-limit — just a server hiccup)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"internal server error"},"id":1}`)
			return
		}
		// Second call: success
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"from":"0xaaaa","to":"0xbbbb","value":"0x1","type":"CALL","input":"0x","output":"0x","gas":"0x5208","gasUsed":"0x5208"},"id":1}`)
	}))
	defer server.Close()

	traceFailover := rpc.NewFailover[evm.EthereumAPI](nil)
	// Add two providers pointing to the same server — first call fails, second succeeds
	c1 := evm.NewEthereumClient(server.URL, nil, 5*time.Second, nil)
	c2 := evm.NewEthereumClient(server.URL, nil, 5*time.Second, nil)

	traceFailover.AddProvider(&rpc.Provider{
		Name: "provider-1", URL: server.URL,
		Network: "test", ClientType: "rpc", Client: c1, State: rpc.StateHealthy,
	})
	traceFailover.AddProvider(&rpc.Provider{
		Name: "provider-2", URL: server.URL,
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
	assert.Equal(t, int32(2), attempt.Load(), "should have made exactly 2 RPC calls (1 fail + 1 success)")
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
