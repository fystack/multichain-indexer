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
		Transactions: []solana.BlockTxn{
			{
				Meta:        tx.Meta,
				Transaction: tx.Transaction,
			},
		},
	}
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

	t.Logf("Transfer: from=%s to=%s amount=%s token=%s",
		tokenTransfer.FromAddress, tokenTransfer.ToAddress,
		tokenTransfer.Amount, tokenTransfer.AssetAddress)
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

	t.Logf("TransferChecked: from=%s to=%s amount=%s token=%s",
		tokenTransfer.FromAddress, tokenTransfer.ToAddress,
		tokenTransfer.Amount, tokenTransfer.AssetAddress)
}
