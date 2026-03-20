package evm

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEndToEnd_BatchSendNative_TransferIndex verifies that the batch tx from issue #71
// (12 internal transfers) gets unique sequential TransferIndex values.
func TestEndToEnd_BatchSendNative_TransferIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTraceClient(t)

	batchTxHash := "0x3659bdb7f7ee48701603159e20357986bdfc3da87428d505841475f212a8369b"
	batchBlockNum := uint64(24669397)

	trace, err := c.DebugTraceTransaction(ctx, batchTxHash)
	require.NoError(t, err)
	require.NotNil(t, trace)

	tx := Txn{
		Hash:             batchTxHash,
		From:             "0x2b3fed49557bd88f78b898684f82fbb355305dbb",
		To:               "0x09c30cdcdd971423cb3ba757a47d56c35d06d818",
		Value:            "0x129f602145be8c00",
		Input:            "0x57b1c066",
		TransactionIndex: "0x42", // example tx index
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "ethereum", batchBlockNum, 1000)

	require.GreaterOrEqual(t, len(transfers), 11, "should extract at least 11 internal transfers")

	// Verify all TransferIndex values are unique
	seen := make(map[string]bool)
	for i, tr := range transfers {
		assert.NotEmpty(t, tr.TransferIndex, "transfer %d should have TransferIndex", i)
		assert.False(t, seen[tr.TransferIndex], "transfer %d has duplicate TransferIndex: %s", i, tr.TransferIndex)
		seen[tr.TransferIndex] = true

		// Verify format: txIndex:trace:N
		assert.Contains(t, tr.TransferIndex, ":trace:", "should have trace format")
		assert.Equal(t, constant.TxTypeNativeTransfer, tr.Type)

		t.Logf("  [%d] TransferIndex=%s from=%s to=%s amount=%s",
			i, tr.TransferIndex, tr.FromAddress, tr.ToAddress, tr.Amount)
	}
}

// TestEndToEnd_SafeTx_TransferIndex verifies Safe tx gets correct TransferIndex format.
func TestEndToEnd_SafeTx_TransferIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTraceClient(t)

	trace, err := c.DebugTraceTransaction(ctx, mainnetSafeTxHash)
	require.NoError(t, err)
	require.NotNil(t, trace)

	tx := Txn{
		Hash:             mainnetSafeTxHash,
		From:             "0xa768d264b8bf98588ebdef6e241a0a73baf287d1",
		To:               "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
		Value:            "0x0",
		Input:            "0x6a761202",
		TransactionIndex: "0xa", // example tx index
	}

	// Test trace-derived transfers
	traceTransfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "ethereum", mainnetSafeBlock, 1000)
	for _, tr := range traceTransfers {
		assert.Contains(t, tr.TransferIndex, ":trace:", "trace transfers should have trace format")
		t.Logf("  trace: TransferIndex=%s amount=%s", tr.TransferIndex, tr.Amount)
	}

	// Test Safe heuristic transfer
	receipt := &TxnReceipt{
		Status:            "0x1",
		GasUsed:           "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
		Logs: []Log{
			{
				Address: tx.To,
				Topics:  []string{SAFE_EXECUTION_SUCCESS_TOPIC, "0x0"},
			},
		},
	}

	// Build the full Safe input for extraction (need full input, not just selector)
	safeTx := Txn{
		Hash:             mainnetSafeTxHash,
		From:             tx.From,
		To:               tx.To,
		Value:            "0x0",
		Input:            safeExecInput, // use the full test input constant
		TransactionIndex: "0xa",
	}

	safeTransfers := ExtractSafeTransfers(safeTx, receipt, "ethereum", mainnetSafeBlock, 1000)
	require.Len(t, safeTransfers, 1)
	assert.Equal(t, "10:safe:0", safeTransfers[0].TransferIndex) // 0xa = 10
	t.Logf("  safe: TransferIndex=%s amount=%s", safeTransfers[0].TransferIndex, safeTransfers[0].Amount)
}
