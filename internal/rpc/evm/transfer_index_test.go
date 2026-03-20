package evm

import (
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHexIndexToDecimal(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"0x5", "5"},
		{"0x0", "0"},
		{"0xa", "10"},
		{"0xff", "255"},
		{"0x64", "100"},
		{"", "0"},
		{"garbage", "0"},
		{"0x", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, hexIndexToDecimal(tt.input))
		})
	}
}

func TestExtractTransfers_TransferIndex_NativeTransfer(t *testing.T) {
	tx := Txn{
		Hash:             "0xabc",
		From:             "0xaaaa",
		To:               "0xbbbb",
		Value:            "0xde0b6b3a7640000", // 1 ETH
		Input:            "",
		TransactionIndex: "0x5",
	}

	transfers := tx.ExtractTransfers("eth", nil, 100, 1000)
	require.Len(t, transfers, 1)
	assert.Equal(t, "5", transfers[0].TransferIndex)
	assert.Equal(t, constant.TxTypeNativeTransfer, transfers[0].Type)
}

func TestExtractTransfers_TransferIndex_ERC20Log(t *testing.T) {
	tx := Txn{
		Hash:             "0xabc",
		From:             "0xaaaa",
		To:               "0xtoken",
		Value:            "0x0",
		Input:            ERC20_TRANSFER_SIG + "0000000000000000000000000000000000000000000000000000000000000000",
		TransactionIndex: "0x3",
	}

	receipt := &TxnReceipt{
		Status:            "0x1",
		GasUsed:           "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
		Logs: []Log{
			{
				Address: "0xtoken",
				Topics: []string{
					ERC20_TRANSFER_TOPIC,
					"0x000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"0x000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				},
				Data:     "0x0000000000000000000000000000000000000000000000000de0b6b3a7640000",
				LogIndex: "0xc", // 12
			},
		},
	}

	transfers := tx.ExtractTransfers("eth", receipt, 100, 1000)

	var erc20 *types.Transaction
	for i, tr := range transfers {
		if tr.Type == "erc20_transfer" {
			erc20 = &transfers[i]
			break
		}
	}
	require.NotNil(t, erc20, "should find ERC20 transfer")
	assert.Equal(t, "3:12", erc20.TransferIndex) // txIndex:logIndex
}

func TestExtractTransfers_TransferIndex_ERC20Input(t *testing.T) {
	// ERC20 transfer from input (no receipt)
	to := "000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	amount := "0000000000000000000000000000000000000000000000000de0b6b3a7640000"

	tx := Txn{
		Hash:             "0xabc",
		From:             "0xaaaa",
		To:               "0xtoken",
		Value:            "0x0",
		Input:            ERC20_TRANSFER_SIG + to + amount,
		TransactionIndex: "0x7",
	}

	transfers := tx.ExtractTransfers("eth", nil, 100, 1000)
	require.Len(t, transfers, 1)
	assert.Equal(t, "7", transfers[0].TransferIndex) // txIndex only, no logIndex
	assert.Equal(t, constant.TxTypeTokenTransfer, transfers[0].Type)
}

func TestExtractSafeTransfers_TransferIndex(t *testing.T) {
	tx := Txn{
		Hash:             "0xabc",
		From:             "0xsender",
		To:               "0xsafe",
		Value:            "0x0",
		Input:            safeExecInput,
		TransactionIndex: "0x9",
	}

	receipt := makeSuccessReceipt("0xabc", "0xsafe")
	transfers := ExtractSafeTransfers(tx, receipt, "eth", 100, 1000)

	require.Len(t, transfers, 1)
	assert.Equal(t, "9:safe:0", transfers[0].TransferIndex)
	assert.Equal(t, constant.TxTypeNativeTransfer, transfers[0].Type)
}

func TestExtractInternalTransfers_TransferIndex(t *testing.T) {
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
			{
				From:  "0xbbbb",
				To:    "0xdddd",
				Value: "0x1",
				Type:  "CREATE",
			},
		},
	}

	tx := Txn{
		Hash:             "0xabc",
		From:             "0xaaaa",
		To:               "0xbbbb",
		Input:            "0x12345678",
		TransactionIndex: "0x2",
	}

	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	require.Len(t, transfers, 3)
	assert.Equal(t, "2:trace:0", transfers[0].TransferIndex)
	assert.Equal(t, "2:trace:1", transfers[1].TransferIndex)
	assert.Equal(t, "2:trace:2", transfers[2].TransferIndex)
}

func TestExtractInternalTransfers_TransferIndex_Ordering(t *testing.T) {
	// Depth-first walk: root → child1 → grandchild → child2
	trace := &CallTrace{
		From:  "0xaaaa",
		To:    "0xbbbb",
		Value: "0x1",
		Type:  "CALL",
		Input: "0xabcd",
		Calls: []CallTrace{
			{
				From:  "0xbbbb",
				To:    "0xcccc",
				Value: "0x2",
				Type:  "CALL",
				Calls: []CallTrace{
					{
						From:  "0xcccc",
						To:    "0xdddd",
						Value: "0x3",
						Type:  "CALL",
					},
				},
			},
			{
				From:  "0xbbbb",
				To:    "0xeeee",
				Value: "0x4",
				Type:  "CALL",
			},
		},
	}

	tx := Txn{Hash: "0x1", From: "0xaaaa", To: "0xbbbb", Input: "0xabcd", TransactionIndex: "0x0"}
	transfers := ExtractInternalTransfers(trace, tx, decimal.Zero, "eth", 100, 1000)

	require.Len(t, transfers, 4)
	// DFS order: root(0) → child1(1) → grandchild(2) → child2(3)
	assert.Equal(t, "0:trace:0", transfers[0].TransferIndex)
	assert.Equal(t, "0:trace:1", transfers[1].TransferIndex)
	assert.Equal(t, "0:trace:2", transfers[2].TransferIndex)
	assert.Equal(t, "0:trace:3", transfers[3].TransferIndex)

	// Verify amounts match DFS order
	assert.Equal(t, "1", transfers[0].Amount)   // root
	assert.Equal(t, "2", transfers[1].Amount)   // child1
	assert.Equal(t, "3", transfers[2].Amount)   // grandchild
	assert.Equal(t, "4", transfers[3].Amount)   // child2
}

func TestDedupTransfers_SameAmountDifferentIndex(t *testing.T) {
	transfers := []types.Transaction{
		{TxHash: "0x1", Type: "native_transfer", FromAddress: "0xa", ToAddress: "0xb", Amount: "100", BlockNumber: 1, TransferIndex: "5:trace:0"},
		{TxHash: "0x1", Type: "native_transfer", FromAddress: "0xa", ToAddress: "0xb", Amount: "100", BlockNumber: 1, TransferIndex: "5:trace:1"},
	}

	result := utils.DedupTransfers(transfers)
	assert.Len(t, result, 2, "different TransferIndex should NOT be deduped")
}

func TestDedupTransfers_SameIndex(t *testing.T) {
	transfers := []types.Transaction{
		{TxHash: "0x1", Type: "native_transfer", FromAddress: "0xa", ToAddress: "0xb", Amount: "100", BlockNumber: 1, TransferIndex: "5:trace:0"},
		{TxHash: "0x1", Type: "native_transfer", FromAddress: "0xa", ToAddress: "0xb", Amount: "100", BlockNumber: 1, TransferIndex: "5:trace:0"},
	}

	result := utils.DedupTransfers(transfers)
	assert.Len(t, result, 1, "same TransferIndex should be deduped")
}
