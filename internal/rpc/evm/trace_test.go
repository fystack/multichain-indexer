package evm

import (
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractInternalTransfers_NestedCALLs(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0xde0b6b3a7640000", // 1 ETH
		Type:  "CALL",
		Input: "0x12345678", // contract call, not plain transfer
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0xcccc",
				Value: "0x6f05b59d3b20000", // 0.5 ETH
				Type:  "CALL",
			},
			{
				From:  "0xbbbb",
				To:    "0xdddd",
				Value: "0x0", // no value
				Type:  "CALL",
			},
		},
	}

	tx := Txn{
		Hash:  "0xtx1",
		From:  "0xaaaa",
		To:    "0xbbbb",
		Input: "0x12345678",
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// Root CALL (contract call with value) + child CALL with value = 2 transfers
	// Child with value=0 is skipped
	require.Len(t, transfers, 2)

	assert.Equal(t, "1000000000000000000", transfers[0].Amount)
	assert.Equal(t, ToChecksumAddress("0xaaaa"), transfers[0].FromAddress)
	assert.Equal(t, ToChecksumAddress("0xbbbb"), transfers[0].ToAddress)
	assert.Equal(t, constant.TxTypeNativeTransfer, transfers[0].Type)

	assert.Equal(t, "500000000000000000", transfers[1].Amount)
	assert.Equal(t, ToChecksumAddress("0xbbbb"), transfers[1].FromAddress)
	assert.Equal(t, ToChecksumAddress("0xcccc"), transfers[1].ToAddress)
}

func TestExtractInternalTransfers_RootSkipForPlainNativeTransfer(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0xde0b6b3a7640000", // 1 ETH
		Type:  "CALL",
		Input: "0x", // empty input = plain native transfer
	}

	tx := Txn{
		Hash:  "0xtx2",
		From:  "0xaaaa",
		To:    "0xbbbb",
		Input: "", // plain native transfer
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// Root is skipped because ExtractTransfers already captures plain native transfers
	assert.Len(t, transfers, 0)
}

func TestExtractInternalTransfers_RootNotSkippedForContractCallWithValue(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0xde0b6b3a7640000",
		Type:  "CALL",
		Input: "0xabcdef00",
	}

	tx := Txn{
		Hash:  "0xtx3",
		From:  "0xaaaa",
		To:    "0xbbbb",
		Input: "0xabcdef00", // contract call
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// Root is NOT skipped for contract calls with value
	require.Len(t, transfers, 1)
	assert.Equal(t, "1000000000000000000", transfers[0].Amount)
}

func TestExtractInternalTransfers_CREATEWithValue(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xnewcontract",
		Value: "0xde0b6b3a7640000",
		Type:  "CREATE",
		Input: "0x6080604052...", // bytecode
	}

	// CREATE tx: To is empty
	tx := Txn{
		Hash:  "0xtx4",
		From:  "0xaaaa",
		To:    "",
		Input: "0x6080604052...",
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// CREATE with value is extracted — ExtractTransfers doesn't emit native for CREATE txs
	require.Len(t, transfers, 1)
	assert.Equal(t, "1000000000000000000", transfers[0].Amount)
	assert.Equal(t, ToChecksumAddress("0xaaaa"), transfers[0].FromAddress)
	assert.Equal(t, ToChecksumAddress("0xnewcontract"), transfers[0].ToAddress)
}

func TestExtractInternalTransfers_CREATE2WithValue(t *testing.T) {
	trace := &CallTrace{
		From:  "0xfactory",
		To:    "0xdeployed",
		Value: "0x1",
		Type:  "CREATE2",
	}

	tx := Txn{
		Hash:  "0xtx5",
		From:  "0xfactory",
		To:    "",
		Input: "0x6080...",
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)
	require.Len(t, transfers, 1)
	assert.Equal(t, "1", transfers[0].Amount)
}

func TestExtractInternalTransfers_SkipDELEGATECALL(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0x0",
		Type:  "CALL",
		Input: "0x12345678",
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0ximpl",
				Value: "0xde0b6b3a7640000",
				Type:  "DELEGATECALL", // should be skipped
			},
		},
	}

	tx := Txn{Hash: "0xtx6", From: "0xaaaa", To: "0xbbbb", Input: "0x12345678"}
	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// DELEGATECALL is skipped even with value
	assert.Len(t, transfers, 0)
}

func TestExtractInternalTransfers_SkipSTATICCALL(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0x0",
		Type:  "CALL",
		Input: "0x12345678",
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0xoracle",
				Value: "0x1",
				Type:  "STATICCALL", // should be skipped
			},
		},
	}

	tx := Txn{Hash: "0xtx7", From: "0xaaaa", To: "0xbbbb", Input: "0x12345678"}
	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	assert.Len(t, transfers, 0)
}

func TestExtractInternalTransfers_SkipRevertedCalls(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0x0",
		Type:  "CALL",
		Input: "0x12345678",
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0xcccc",
				Value: "0xde0b6b3a7640000",
				Type:  "CALL",
				Error: "execution reverted",
			},
		},
	}

	tx := Txn{Hash: "0xtx8", From: "0xaaaa", To: "0xbbbb", Input: "0x12345678"}
	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// Reverted calls are skipped
	assert.Len(t, transfers, 0)
}

func TestExtractInternalTransfers_RevertedParentSkipsChildren(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0x0",
		Type:  "CALL",
		Input: "0x12345678",
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0xcccc",
				Value: "0x1",
				Type:  "CALL",
				Error: "out of gas",
				Calls: []CallTrace{
					{
						From:  "0xcccc",
						To:    "0xdddd",
						Value: "0x2",
						Type:  "CALL",
						// No error on child, but parent reverted
					},
				},
			},
		},
	}

	tx := Txn{Hash: "0xtx9", From: "0xaaaa", To: "0xbbbb", Input: "0x12345678"}
	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	// Parent reverted → entire subtree skipped (walkCallTrace returns on Error != "")
	assert.Len(t, transfers, 0)
}

func TestExtractInternalTransfers_NilTrace(t *testing.T) {
	tx := Txn{Hash: "0xtx10", From: "0xaaaa", To: "0xbbbb", Input: "0x12345678"}
	transfers := ExtractInternalTransfers(nil, tx, decimal.Zero, "eth", 100, 1000)
	assert.Nil(t, transfers)
}

func TestExtractInternalTransfers_DedupWithExtractTransfers(t *testing.T) {
	// Simulate: trace emits root CALL transfer + ExtractTransfers also emits same native transfer
	// DedupTransfers should remove the duplicate

	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0xde0b6b3a7640000",
		Type:  "CALL",
		Input: "0x12345678",
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0xcccc",
				Value: "0x6f05b59d3b20000",
				Type:  "CALL",
			},
		},
	}

	tx := Txn{
		Hash:  "0xtx11",
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0xde0b6b3a7640000",
		Input: "0x12345678",
	}

	fee := decimal.Zero
	traceTransfers := ExtractInternalTransfers(trace, tx, fee, "eth", 100, 1000)
	topLevelTransfers := tx.ExtractTransfers("eth", nil, 100, 1000)

	combined := append(traceTransfers, topLevelTransfers...)
	deduped := utils.DedupTransfers(combined)

	// trace emits 2 (root + child), ExtractTransfers emits 0 (no receipt, contract call input)
	// After dedup: 2 unique transfers
	assert.Len(t, deduped, 2)
}

func TestExtractInternalTransfers_AllTransfersAreNativeType(t *testing.T) {
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0x1",
		Type:  "CALL",
		Input: "0xabcd",
		Calls: []CallTrace{
			{From: "0xbbbb", To: "0xcccc", Value: "0x2", Type: "CALL"},
			{From: "0xcccc", To: "0xdddd", Value: "0x3", Type: "CREATE"},
		},
	}

	tx := Txn{Hash: "0xtx12", From: "0xaaaa", To: "0xbbbb", Input: "0xabcd"}
	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	require.Len(t, transfers, 3)
	for _, tr := range transfers {
		assert.Equal(t, constant.TxTypeNativeTransfer, tr.Type, "all trace transfers must be native_transfer")
	}
}
