package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitBlock_TwoWayIndexing_UniqueHashes simulates emitBlock behavior:
// when both addresses are monitored, two events are emitted for the same transfer
// (in + out). They must have different Hash() values for NATS dedup.
func TestEmitBlock_TwoWayIndexing_UniqueHashes(t *testing.T) {
	// Simulate a transfer where both from and to are monitored
	baseTx := Transaction{
		TxHash:        "0xabc",
		NetworkId:     "ethereum",
		BlockNumber:   100,
		BlockHash:     "0xblockhash",
		TransferIndex: "5:trace:0",
		FromAddress:   "0xmonitored_sender",
		ToAddress:     "0xmonitored_receiver",
		Amount:        "1000000000000000000",
	}

	// emitBlock creates two events: in and out
	inTx := baseTx
	inTx.Direction = DirectionIn

	outTx := baseTx
	outTx.Direction = DirectionOut

	// Both should have valid hashes
	require.NotEmpty(t, inTx.Hash())
	require.NotEmpty(t, outTx.Hash())

	// Hashes must differ — otherwise NATS dedup drops one
	assert.NotEqual(t, inTx.Hash(), outTx.Hash(),
		"in/out events for same transfer must have different Hash() for NATS dedup")
}

// TestEmitBlock_DuplicateAmountTransfers_UniqueHashes simulates a batch tx
// that sends the same amount to the same address twice (different positions).
// Both must be enqueued — different TransferIndex → different Hash().
func TestEmitBlock_DuplicateAmountTransfers_UniqueHashes(t *testing.T) {
	// Batch contract sends 0.1 ETH to 0xrecipient twice in one tx
	transfer1 := Transaction{
		TxHash:        "0xbatch",
		NetworkId:     "ethereum",
		BlockNumber:   200,
		BlockHash:     "0xblockhash",
		TransferIndex: "3:trace:0", // first internal call
		FromAddress:   "0xcontract",
		ToAddress:     "0xrecipient",
		Amount:        "100000000000000000", // 0.1 ETH
		Direction:     DirectionIn,
	}

	transfer2 := Transaction{
		TxHash:        "0xbatch",
		NetworkId:     "ethereum",
		BlockNumber:   200,
		BlockHash:     "0xblockhash",
		TransferIndex: "3:trace:5", // fifth internal call — same recipient, same amount
		FromAddress:   "0xcontract",
		ToAddress:     "0xrecipient",
		Amount:        "100000000000000000", // 0.1 ETH
		Direction:     DirectionIn,
	}

	assert.NotEqual(t, transfer1.Hash(), transfer2.Hash(),
		"same amount to same address at different positions must have different Hash()")

	// Also verify: if both were emitted as out direction, still different
	transfer1.Direction = DirectionOut
	transfer2.Direction = DirectionOut
	assert.NotEqual(t, transfer1.Hash(), transfer2.Hash(),
		"different TransferIndex should produce different Hash regardless of direction")
}

// TestEmitBlock_MixedSources_UniqueHashes verifies that transfers from different
// extraction sources (trace, Safe, ERC20) don't collide even in the same tx.
func TestEmitBlock_MixedSources_UniqueHashes(t *testing.T) {
	base := Transaction{
		TxHash:      "0xmixed",
		NetworkId:   "ethereum",
		BlockNumber: 300,
		BlockHash:   "0xblockhash",
		FromAddress: "0xa",
		ToAddress:   "0xb",
		Amount:      "100",
		Direction:   DirectionIn,
	}

	trace := base
	trace.TransferIndex = "7:trace:0"

	safe := base
	safe.TransferIndex = "7:safe:0"

	erc20 := base
	erc20.TransferIndex = "7:12" // txIndex:logIndex

	native := base
	native.TransferIndex = "7"

	hashes := map[string]string{
		"trace":  trace.Hash(),
		"safe":   safe.Hash(),
		"erc20":  erc20.Hash(),
		"native": native.Hash(),
	}

	// All must be unique
	seen := make(map[string]string)
	for source, h := range hashes {
		if existingSource, exists := seen[h]; exists {
			t.Errorf("hash collision between %s and %s", source, existingSource)
		}
		seen[h] = source
	}
}
