package evm

import (
	"strings"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real execTransaction input from tx 0x7c98ff7c...
// Encodes: to=0xc26dC13d057824342D5480b153f288bd1C5e3e9d, value=100000000000000000 (0.1 ETH),
// data=empty, operation=0 (Call)
const safeExecInput = "0x6a761202000000000000000000000000c26dc13d057824342d5480b153f288bd1c5e3e9d000000000000000000000000000000000000000000000000016345785d8a000000000000000000000000000000000000000000000000000000000000000001400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001600000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000008200000000000000000000000040a1ade6e21129c09cfb3c501efba5e56ba9a3e50000000000000000000000000000000000000000000000000000000000000000010000000000000000000000005c45ee16de1570b662e32e7960e1e501dd93cde4000000000000000000000000000000000000000000000000000000000000000001a5aa81e56a92b6f46bf78edb6a34e0086c0bb5c1e9d2c20ae60d49c1a8de6b4f15c14847f1ba3f6c35b51dbefae4c74a01bf7c28a0a0c53fb4e73a6d40e36f76b1c00000000000000000000000000000000000000000000000000000000000000"

const safeContractAddr = "0x84ba2321d46814fb1aa69a7b71882efea50f700c"

func makeSuccessReceipt(txHash, safeAddr string) *TxnReceipt {
	return &TxnReceipt{
		TransactionHash:   txHash,
		GasUsed:           "0x1e848",
		EffectiveGasPrice: "0x3b9aca00",
		Status:            "0x1",
		Logs: []Log{
			{
				Address: safeAddr,
				Topics:  []string{SAFE_EXECUTION_SUCCESS_TOPIC, "0x0000000000000000000000000000000000000000000000000000000000000000"},
				Data:    "0x0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
}

func makeFailureReceipt(txHash, safeAddr string) *TxnReceipt {
	return &TxnReceipt{
		TransactionHash:   txHash,
		GasUsed:           "0x1e848",
		EffectiveGasPrice: "0x3b9aca00",
		Status:            "0x1",
		Logs: []Log{
			{
				Address: safeAddr,
				Topics:  []string{SAFE_EXECUTION_FAILURE_TOPIC, "0x0000000000000000000000000000000000000000000000000000000000000000"},
				Data:    "0x0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
}

func safeTx() Txn {
	return Txn{
		Hash:  "0xabc123",
		From:  "0xa768d264b8bf98588ebdef6e241a0a73baf287d1",
		To:    safeContractAddr,
		Value: "0x0",
		Input: safeExecInput,
	}
}

// --- IsSafeExecTransaction tests ---

func TestIsSafeExecTransaction(t *testing.T) {
	assert.True(t, IsSafeExecTransaction("0x6a761202000000000000000000000000c26dc13d"))
	assert.True(t, IsSafeExecTransaction(safeExecInput))
	assert.False(t, IsSafeExecTransaction("0xa9059cbb000000000000000000000000c26dc13d"))
	assert.False(t, IsSafeExecTransaction("0xdeadbeef"))
	assert.False(t, IsSafeExecTransaction("0x6a7612")) // too short
	assert.False(t, IsSafeExecTransaction(""))
	assert.False(t, IsSafeExecTransaction("0x"))
}

// --- Decode tests ---

func TestDecodeGnosisSafeExecTransaction(t *testing.T) {
	params, err := DecodeGnosisSafeExecTransaction(safeExecInput)
	require.NoError(t, err)

	assert.Equal(t, ToChecksumAddress("0xc26dc13d057824342d5480b153f288bd1c5e3e9d"), params.To)
	assert.Equal(t, "100000000000000000", params.Value.String()) // 0.1 ETH
	assert.Empty(t, params.Data, "data should be empty for pure ETH transfer")
	assert.Equal(t, uint8(0), params.Operation, "operation should be Call (0)")
}

func TestDecodeGnosisSafe_MalformedInput(t *testing.T) {
	// Truncated input
	_, err := DecodeGnosisSafeExecTransaction("0x6a761202abcdef")
	assert.Error(t, err, "should reject truncated input")

	// Invalid hex characters
	_, err = DecodeGnosisSafeExecTransaction("0x6a761202" + strings.Repeat("zz", 128))
	assert.Error(t, err, "should reject invalid hex")

	// Empty input
	_, err = DecodeGnosisSafeExecTransaction("")
	assert.Error(t, err, "should reject empty input")

	// Ensure ExtractSafeTransfers doesn't panic on malformed input
	tx := Txn{Hash: "0x1", To: "0x2", Value: "0x0", Input: "0x6a761202abcdef"}
	receipt := makeSuccessReceipt("0x1", "0x2")
	transfers := ExtractSafeTransfers(tx, receipt, "test", 1, 1)
	assert.Empty(t, transfers, "should gracefully handle malformed Safe input")
}

// --- ExtractSafeTransfers tests ---

func TestExtractSafeTransfers_ValidNativeTransfer(t *testing.T) {
	tx := safeTx()
	receipt := makeSuccessReceipt(tx.Hash, safeContractAddr)

	transfers := ExtractSafeTransfers(tx, receipt, "ethereum-mainnet", 22869070, 1700000000)

	require.Len(t, transfers, 1)
	assert.Equal(t, constant.TxTypeNativeTransfer, transfers[0].Type)
	assert.Equal(t, ToChecksumAddress(safeContractAddr), transfers[0].FromAddress)
	assert.Equal(t, ToChecksumAddress("0xc26dc13d057824342d5480b153f288bd1c5e3e9d"), transfers[0].ToAddress)
	assert.Equal(t, "100000000000000000", transfers[0].Amount)
}

func TestExtractSafeTransfers_ExecutionFailure(t *testing.T) {
	tx := safeTx()
	receipt := makeFailureReceipt(tx.Hash, safeContractAddr)

	transfers := ExtractSafeTransfers(tx, receipt, "ethereum-mainnet", 22869070, 1700000000)
	assert.Empty(t, transfers, "should skip when ExecutionFailure in receipt")
}

func TestExtractSafeTransfers_ExecutionSuccessFromWrongAddress(t *testing.T) {
	tx := safeTx()
	// ExecutionSuccess emitted by a DIFFERENT contract, not the Safe (tx.To)
	receipt := makeSuccessReceipt(tx.Hash, "0x1111111111111111111111111111111111111111")

	transfers := ExtractSafeTransfers(tx, receipt, "ethereum-mainnet", 22869070, 1700000000)
	assert.Empty(t, transfers, "should skip when ExecutionSuccess is from a different contract than tx.To")
}

func TestExtractSafeTransfers_DelegateCall(t *testing.T) {
	inputHex := "0x6a761202" +
		"000000000000000000000000c26dc13d057824342d5480b153f288bd1c5e3e9d" +
		"000000000000000000000000000000000000000000000000016345785d8a0000" +
		"0000000000000000000000000000000000000000000000000000000000000140" +
		"0000000000000000000000000000000000000000000000000000000000000001" + // operation=1
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000160" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000"

	tx := Txn{Hash: "0xabc", From: "0xaaa", To: safeContractAddr, Value: "0x0", Input: inputHex}
	receipt := makeSuccessReceipt(tx.Hash, safeContractAddr)

	transfers := ExtractSafeTransfers(tx, receipt, "ethereum-mainnet", 22869070, 1700000000)
	assert.Empty(t, transfers, "should skip DelegateCall operations (operation=1)")
}

func TestExtractSafeTransfers_ZeroValue(t *testing.T) {
	inputHex := "0x6a761202" +
		"000000000000000000000000c26dc13d057824342d5480b153f288bd1c5e3e9d" +
		"0000000000000000000000000000000000000000000000000000000000000000" + // value=0
		"0000000000000000000000000000000000000000000000000000000000000140" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000160" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000"

	tx := Txn{Hash: "0xabc", From: "0xaaa", To: safeContractAddr, Value: "0x0", Input: inputHex}
	receipt := makeSuccessReceipt(tx.Hash, safeContractAddr)

	transfers := ExtractSafeTransfers(tx, receipt, "ethereum-mainnet", 22869070, 1700000000)
	assert.Empty(t, transfers, "should skip when value=0")
}

func TestExtractSafeTransfers_ValueWithData(t *testing.T) {
	// value > 0 AND non-empty data (ETH + contract call) — known limitation: NOT extracted
	// data offset = 0x140 (320 bytes = hex position 640 in params), so word 10 is the length
	inputHex := "0x6a761202" +
		"000000000000000000000000c26dc13d057824342d5480b153f288bd1c5e3e9d" + // to
		"000000000000000000000000000000000000000000000000016345785d8a0000" + // value=0.1 ETH
		"0000000000000000000000000000000000000000000000000000000000000140" + // data offset=0x140=320 bytes
		"0000000000000000000000000000000000000000000000000000000000000000" + // operation=0
		"0000000000000000000000000000000000000000000000000000000000000000" + // safeTxGas
		"0000000000000000000000000000000000000000000000000000000000000000" + // baseGas
		"0000000000000000000000000000000000000000000000000000000000000000" + // gasPrice
		"0000000000000000000000000000000000000000000000000000000000000000" + // gasToken
		"0000000000000000000000000000000000000000000000000000000000000000" + // refundReceiver
		"0000000000000000000000000000000000000000000000000000000000000160" + // sigs offset
		"0000000000000000000000000000000000000000000000000000000000000004" + // data length=4 (word 10 @ byte offset 320)
		"deadbeef00000000000000000000000000000000000000000000000000000000" // data content

	tx := Txn{Hash: "0xabc", From: "0xaaa", To: safeContractAddr, Value: "0x0", Input: inputHex}
	receipt := makeSuccessReceipt(tx.Hash, safeContractAddr)

	transfers := ExtractSafeTransfers(tx, receipt, "ethereum-mainnet", 22869070, 1700000000)
	assert.Empty(t, transfers, "ETH+calldata case is not extracted (known limitation)")
}

func TestExtractSafeTransfers_NilReceipt(t *testing.T) {
	tx := safeTx()
	transfers := ExtractSafeTransfers(tx, nil, "ethereum-mainnet", 22869070, 1700000000)
	assert.Empty(t, transfers, "should skip when receipt is nil")
}

// --- HasLogTopicFrom tests ---

func TestHasLogTopicFrom(t *testing.T) {
	otherAddr := "0x1111111111111111111111111111111111111111"

	// Match: correct topic + correct emitter
	receipt := makeSuccessReceipt("0xabc", safeContractAddr)
	assert.True(t, receipt.HasLogTopicFrom(SAFE_EXECUTION_SUCCESS_TOPIC, safeContractAddr))

	// Wrong topic
	assert.False(t, receipt.HasLogTopicFrom(SAFE_EXECUTION_FAILURE_TOPIC, safeContractAddr))

	// Wrong emitter
	assert.False(t, receipt.HasLogTopicFrom(SAFE_EXECUTION_SUCCESS_TOPIC, otherAddr))

	// Failure receipt
	failReceipt := makeFailureReceipt("0xabc", safeContractAddr)
	assert.True(t, failReceipt.HasLogTopicFrom(SAFE_EXECUTION_FAILURE_TOPIC, safeContractAddr))
	assert.False(t, failReceipt.HasLogTopicFrom(SAFE_EXECUTION_SUCCESS_TOPIC, safeContractAddr))

	// Nil receipt
	assert.False(t, (*TxnReceipt)(nil).HasLogTopicFrom(SAFE_EXECUTION_SUCCESS_TOPIC, safeContractAddr))

	// Case-insensitive address matching
	receipt2 := makeSuccessReceipt("0xdef", "0xABCdef1234567890abcdef1234567890ABCDEF12")
	assert.True(t, receipt2.HasLogTopicFrom(SAFE_EXECUTION_SUCCESS_TOPIC, "0xabcdef1234567890abcdef1234567890abcdef12"))
}
