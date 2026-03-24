package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const solanaMainnetRPC = "https://api.mainnet-beta.solana.com"

func newTestSolanaClient() *solana.Client {
	return solana.NewSolanaClient(solanaMainnetRPC, nil, 30*time.Second, nil)
}

func newTestSolanaIndexer() *SolanaIndexer {
	return &SolanaIndexer{
		chainName:   "solana",
		config:      config.ChainConfig{NetworkId: "solana-mainnet"},
		pubkeyStore: nil, // no filtering
	}
}

// txToBlockResult wraps a single GetTransactionResult into a GetBlockResult
// so it can be fed into extractSolanaTransfers.
func txToBlockResult(tx *solana.GetTransactionResult) *solana.GetBlockResult {
	return &solana.GetBlockResult{
		Blockhash:         "testhash123",
		PreviousBlockhash: "parenthash456",
		Transactions: []solana.BlockTxn{
			{
				Meta:        tx.Meta,
				Transaction: tx.Transaction,
			},
		},
	}
}

func TestSolanaBlockHashAndTransferIndex(t *testing.T) {
	idx := newTestSolanaIndexer()

	blockHash := "9xJ7rGWdmA9Y4qKkZn1bFwP3KpvLcAhRsL1oXrNBp4v"
	makeTxnEnvelope := func(sig string, keys []solana.AccountKey, ixs []solana.Instruction) solana.TxnEnvelope {
		env := solana.TxnEnvelope{Signatures: []string{sig}}
		env.Message.AccountKeys = keys
		env.Message.Instructions = ixs
		return env
	}

	block := &solana.GetBlockResult{
		Blockhash:         blockHash,
		PreviousBlockhash: "parentHash123",
		Transactions: []solana.BlockTxn{
			{
				Meta: &solana.TxnMeta{Fee: 5000},
				Transaction: makeTxnEnvelope("sig1",
					[]solana.AccountKey{
						{Pubkey: "sender1"},
						{Pubkey: "receiver1"},
						{Pubkey: "sender2"},
						{Pubkey: "receiver2"},
						{Pubkey: solanaSystemProgramID},
					},
					[]solana.Instruction{
						{
							Program:   "system",
							ProgramId: solanaSystemProgramID,
							Parsed: map[string]any{
								"type": "transfer",
								"info": map[string]any{
									"source":      "sender1",
									"destination": "receiver1",
									"lamports":    float64(1000000),
								},
							},
						},
						{
							Program:   "system",
							ProgramId: solanaSystemProgramID,
							Parsed: map[string]any{
								"type": "transfer",
								"info": map[string]any{
									"source":      "sender2",
									"destination": "receiver2",
									"lamports":    float64(2000000),
								},
							},
						},
					},
				),
			},
			{
				Meta: &solana.TxnMeta{Fee: 5000},
				Transaction: makeTxnEnvelope("sig2",
					[]solana.AccountKey{
						{Pubkey: "sender3"},
						{Pubkey: "receiver3"},
						{Pubkey: solanaSystemProgramID},
					},
					[]solana.Instruction{
						{
							Program:   "system",
							ProgramId: solanaSystemProgramID,
							Parsed: map[string]any{
								"type": "transfer",
								"info": map[string]any{
									"source":      "sender3",
									"destination": "receiver3",
									"lamports":    float64(3000000),
								},
							},
						},
					},
				),
			},
		},
	}

	transfers := idx.extractSolanaTransfers("solana-mainnet", 100, 1234567890, block)

	require.Len(t, transfers, 3)

	// All transfers should have BlockHash set
	for _, tx := range transfers {
		assert.Equal(t, blockHash, tx.BlockHash, "BlockHash should be propagated")
		assert.NotEmpty(t, tx.TransferIndex, "TransferIndex should be set")
	}

	// TransferIndexes should be unique
	seen := map[string]bool{}
	for _, tx := range transfers {
		key := tx.TxHash + ":" + tx.TransferIndex
		assert.False(t, seen[key], "TransferIndex should be unique within block, duplicate: %s", key)
		seen[key] = true
	}

	// First tx has two transfers: 0:0 and 0:1
	assert.Equal(t, "0:0", transfers[0].TransferIndex)
	assert.Equal(t, "0:1", transfers[1].TransferIndex)
	// Second tx has one transfer: 1:0
	assert.Equal(t, "1:0", transfers[2].TransferIndex)
}

// TestParseSPLTransfer tests parsing of SPL Transfer (opcode 3) instruction from a real mainnet tx.
// Transaction: 4dc8JLGc2ee2FHXhEfDEXNuG62TZjwvSUGiCwfPnXpiMfCEAcTjg6LXnqEAV9fzbHXaWAiNcNEDrSQMWYmfy9cTv
// This is a USDC transfer using the Transfer instruction (not TransferChecked).
func TestParseSPLTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTestSolanaClient()

	txSig := "4dc8JLGc2ee2FHXhEfDEXNuG62TZjwvSUGiCwfPnXpiMfCEAcTjg6LXnqEAV9fzbHXaWAiNcNEDrSQMWYmfy9cTv"
	txResult, err := c.GetTransaction(ctx, txSig)
	require.NoError(t, err)
	require.NotNil(t, txResult, "transaction should exist on mainnet")

	idx := newTestSolanaIndexer()
	block := txToBlockResult(txResult)

	ts := uint64(0)
	if txResult.BlockTime != nil {
		ts = uint64(*txResult.BlockTime)
	}
	transfers := idx.extractSolanaTransfers("solana-mainnet", txResult.Slot, ts, block)

	// Find the token transfer
	var tokenTransfer *types.Transaction
	for i := range transfers {
		if transfers[i].Type == constant.TxTypeTokenTransfer {
			tokenTransfer = &transfers[i]
			break
		}
	}

	require.NotNil(t, tokenTransfer, "should find a token transfer")

	assert.Equal(t, txSig, tokenTransfer.TxHash, "TxHash should match")
	assert.Equal(t, "382649225", tokenTransfer.Amount, "Amount should be 382649225 (382.649225 USDC)")
	assert.Equal(t, "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", tokenTransfer.AssetAddress, "AssetAddress should be USDC mint")
	assert.NotEmpty(t, tokenTransfer.FromAddress, "FromAddress (owner) should be resolved")
	assert.NotEmpty(t, tokenTransfer.ToAddress, "ToAddress (owner) should be resolved")
	assert.Equal(t, "testhash123", tokenTransfer.BlockHash, "BlockHash should be propagated from block")
	assert.NotEmpty(t, tokenTransfer.TransferIndex, "TransferIndex should be set")

	t.Logf("Transfer: from=%s to=%s amount=%s token=%s transferIndex=%s",
		tokenTransfer.FromAddress, tokenTransfer.ToAddress,
		tokenTransfer.Amount, tokenTransfer.AssetAddress, tokenTransfer.TransferIndex)
}

// TestParseSPLTransferChecked tests parsing of SPL TransferChecked (opcode 12) instruction from a real mainnet tx.
// Transaction: Nmey7zZmsnUCECyfGy3x2GV8YBuYn5s3a7j1s7Qfck8WExaa6mZpQxdqX8pJaDQzS87UaaWomkHcYuFZUypY8C5
// This is a USDC transfer using the TransferChecked instruction.
func TestParseSPLTransferChecked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTestSolanaClient()

	txSig := "Nmey7zZmsnUCECyfGy3x2GV8YBuYn5s3a7j1s7Qfck8WExaa6mZpQxdqX8pJaDQzS87UaaWomkHcYuFZUypY8C5"
	txResult, err := c.GetTransaction(ctx, txSig)
	require.NoError(t, err)
	require.NotNil(t, txResult, "transaction should exist on mainnet")

	idx := newTestSolanaIndexer()
	block := txToBlockResult(txResult)

	ts := uint64(0)
	if txResult.BlockTime != nil {
		ts = uint64(*txResult.BlockTime)
	}
	transfers := idx.extractSolanaTransfers("solana-mainnet", txResult.Slot, ts, block)

	// Find the token transfer
	var tokenTransfer *types.Transaction
	for i := range transfers {
		if transfers[i].Type == constant.TxTypeTokenTransfer {
			tokenTransfer = &transfers[i]
			break
		}
	}

	require.NotNil(t, tokenTransfer, "should find a token transfer")

	assert.Equal(t, txSig, tokenTransfer.TxHash, "TxHash should match")
	assert.Equal(t, "1000000", tokenTransfer.Amount, "Amount should be 1000000 (1 USDC)")
	assert.Equal(t, "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", tokenTransfer.AssetAddress, "AssetAddress should be USDC mint")
	assert.NotEmpty(t, tokenTransfer.FromAddress, "FromAddress (owner) should be resolved")
	assert.NotEmpty(t, tokenTransfer.ToAddress, "ToAddress (owner) should be resolved")
	assert.Equal(t, "testhash123", tokenTransfer.BlockHash, "BlockHash should be propagated from block")
	assert.NotEmpty(t, tokenTransfer.TransferIndex, "TransferIndex should be set")

	t.Logf("TransferChecked: from=%s to=%s amount=%s token=%s transferIndex=%s",
		tokenTransfer.FromAddress, tokenTransfer.ToAddress,
		tokenTransfer.Amount, tokenTransfer.AssetAddress, tokenTransfer.TransferIndex)
}
