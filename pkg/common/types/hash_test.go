package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTransactionHash_IncludesTransferIndex(t *testing.T) {
	tx1 := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock1",
		TransferIndex: "5:trace:0",
		FromAddress:   "0xa",
		ToAddress:     "0xb",
		Direction:     "in",
	}
	tx2 := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock1",
		TransferIndex: "5:trace:1",
		FromAddress:   "0xa",
		ToAddress:     "0xb",
		Direction:     "in",
	}

	assert.NotEqual(t, tx1.Hash(), tx2.Hash(), "different TransferIndex should produce different hashes")
}

func TestTransactionHash_TwoWayIndexing_DirectionDiffers(t *testing.T) {
	base := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock1",
		TransferIndex: "5:trace:0",
		FromAddress:   "0xa",
		ToAddress:     "0xb",
	}

	inTx := base
	inTx.Direction = "in"

	outTx := base
	outTx.Direction = "out"

	assert.NotEqual(t, inTx.Hash(), outTx.Hash(), "in/out should produce different hashes")
}

func TestTransactionHash_SamePositionSameHash(t *testing.T) {
	tx1 := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock1",
		TransferIndex: "5:trace:0",
		Direction:     "in",
	}
	tx2 := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock1",
		TransferIndex: "5:trace:0",
		Direction:     "in",
	}

	assert.Equal(t, tx1.Hash(), tx2.Hash(), "same position should produce same hash (idempotent)")
}

func TestTransactionHash_NoTransferIndex_FallbackBehavior(t *testing.T) {
	// Non-EVM: different from/to should produce different hashes
	tx1 := Transaction{
		NetworkId: "btc",
		TxHash:    "0xabc",
		BlockHash: "0xblock1",
		// TransferIndex empty — non-EVM
		FromAddress: "0xa",
		ToAddress:   "0xb",
		Timestamp:   1000,
		Direction:   "in",
	}
	tx2 := Transaction{
		NetworkId:   "btc",
		TxHash:      "0xabc",
		BlockHash:   "0xblock1",
		FromAddress: "0xa",
		ToAddress:   "0xc", // different
		Timestamp:   1000,
		Direction:   "in",
	}

	assert.NotEqual(t, tx1.Hash(), tx2.Hash(), "non-EVM fallback: different addresses should produce different hashes")
}

func TestTransactionHash_NoTransferIndex_SameAddresses_SameHash(t *testing.T) {
	tx1 := Transaction{
		NetworkId:   "btc",
		TxHash:      "0xabc",
		BlockHash:   "0xblock1",
		FromAddress: "0xa",
		ToAddress:   "0xb",
		Timestamp:   1000,
		Direction:   "in",
	}
	tx2 := Transaction{
		NetworkId:   "btc",
		TxHash:      "0xabc",
		BlockHash:   "0xblock1",
		FromAddress: "0xa",
		ToAddress:   "0xb",
		Timestamp:   1000,
		Direction:   "in",
	}

	assert.Equal(t, tx1.Hash(), tx2.Hash(), "non-EVM fallback: same everything should produce same hash")
}

func TestTransactionHash_ReorgProducesDifferentHash(t *testing.T) {
	// Same tx, same index, different block hash (reorg)
	tx1 := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock_original",
		TransferIndex: "5:trace:0",
		Direction:     "in",
	}
	tx2 := Transaction{
		NetworkId:     "eth",
		TxHash:        "0xabc",
		BlockHash:     "0xblock_after_reorg",
		TransferIndex: "5:trace:0",
		Direction:     "in",
	}

	assert.NotEqual(t, tx1.Hash(), tx2.Hash(), "reorg (different BlockHash) should produce different hash for NATS re-delivery")
}
