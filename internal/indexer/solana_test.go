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

// TestParseBatchSOLTransfer tests parsing of a batch transfer transaction containing
// 20 native SOL transfers (1 lamport each) from a single sender to 20 different recipients.
// Transaction: 5dKRr7cF6pNiuhKYY8RDF5rxTjbFWFGAzKgBXoYrPcg2uMx14uwfopiX1XiyZuu3aihMMRqzSSX9jQcWnamqw5DK
func TestParseBatchSOLTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTestSolanaClient()

	txSig := "5dKRr7cF6pNiuhKYY8RDF5rxTjbFWFGAzKgBXoYrPcg2uMx14uwfopiX1XiyZuu3aihMMRqzSSX9jQcWnamqw5DK"
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

	// This transaction contains 20 native SOL transfers of 1 lamport each
	expectedRecipients := []string{
		"5P1E2RDvWWsQYtHgC9Ng3SejPq7tLdk697tNWfkxwx5g",
		"JCxSuQwRa2Qbtxjrv9Csf2B8AtCMiLjfr24Bb27yheFL",
		"4Y84ZayuVutuW2e1Lq8qfq5JYwYkTn8CtutmZs3jW8oM",
		"9rRE1bEPcQxf3LSJphSd1jHbDVi4dLYWgY9g61YTCHdW",
		"3dUBc9cLiF92uWCdZVGmB7iKPTHVioeE5UqA8PEjUYXs",
		"FeAuJTC8wZypEuTQteys9PRdBPAL3ik8VuViDtQNm2tb",
		"BMvWVyMFoDhfwvqDG4q2uGmnP4sr4FhqT1hDSPbrbGRX",
		"3KSTPVNHmWvkwy3JcuKQH8sLogv1Gf9rauj8Z1TpPdbG",
		"DHY4ANQod7Ff67ojZPgzWePwHNSBeJwbWmye2SwR6F3V",
		"B9eWxGfXGsLhMPx65mdFiSayhqVAxWG4TGzaFpuTWQmH",
		"Avr7d3kwUcV8cUhzDmpbVjzPDCni9FSJYyNaDjuFPLZt",
		"HPDN6Ro6gPgYbNgnv7yhFCpyY3uYzT18epFxRFNZ985w",
		"EYidortrJzJGkR3r2gFg2sJVM6WJQNE7yZdzvhndGPVQ",
		"E4oy1PetbUo7UKww5Dmy9gHyNgrtQWPsC5DHBsVu3KQo",
		"H6EJh7pyygR8ghgPxe1j4NE3FC7BuzC4S2Cybw5fJMcg",
		"5E81HKzq7vXPgGcH3ZndGW56DtLBPC9rJD5RmAMdbE3Y",
		"H1PQRtvSEH2HwwtZUnBfxEerwbhPHhUvTCtDF66NSCY4",
		"9mSFaSJgQotHmL14xSMcRgNC9hnidNfGkyJ2fhvizKuj",
		"GLvb7P4q7AkX1j7r4S2fr4uoLWqDLWRsPkJqMMcNR9CE",
		"5XFz3x79Hi8zzQb8RuzNqzoqXkyxRCgDyvjK7BrfCKML",
	}

	const expectedSender = "AZWibhw2cmpVFt4b45XGhiB8vSmYc2LfMGyHE3Lb97cM"

	// All transfers should be native
	var nativeTransfers []types.Transaction
	for _, tx := range transfers {
		if tx.Type == constant.TxTypeNativeTransfer {
			nativeTransfers = append(nativeTransfers, tx)
		}
	}

	require.Len(t, nativeTransfers, 20, "should detect all 20 native SOL transfers in the batch")

	// Verify each transfer
	for i, tx := range nativeTransfers {
		assert.Equal(t, txSig, tx.TxHash, "transfer %d: TxHash should match", i)
		assert.Equal(t, expectedSender, tx.FromAddress, "transfer %d: sender should match", i)
		assert.Equal(t, expectedRecipients[i], tx.ToAddress, "transfer %d: recipient should match", i)
		assert.Equal(t, "1", tx.Amount, "transfer %d: amount should be 1 lamport", i)
		assert.Empty(t, tx.AssetAddress, "transfer %d: AssetAddress should be empty for native SOL", i)
	}

	// Verify no token transfers were detected
	for _, tx := range transfers {
		assert.NotEqual(t, constant.TxTypeTokenTransfer, tx.Type, "should not detect any token transfers")
	}

	t.Logf("Batch transfer: %d native SOL transfers from %s (1 lamport each)", len(nativeTransfers), expectedSender)
}

// TestParseSquadsMultisigTransfer tests parsing of a Squads Multisig (SMPLecH534NA9acpos4G6x7uf3LWbCAwZQE9e8ZekMu)
// transaction that CPI-calls Token-2022 with transferCheckedWithFee.
// Transaction: 2Sqm2FwnSdTLQ5tfbpCKafWiQDwynmDZrkvGxCbpin3mwPHdmRKNDq6drv5rnosp9is86JwScQLnyWHbV9TfYTDj
func TestParseSquadsMultisigTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTestSolanaClient()

	txSig := "2Sqm2FwnSdTLQ5tfbpCKafWiQDwynmDZrkvGxCbpin3mwPHdmRKNDq6drv5rnosp9is86JwScQLnyWHbV9TfYTDj"
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

	// This transaction contains a Token-2022 transferCheckedWithFee via Squads multisig CPI.
	// Token: HeLp6NuQkmYB4pYWo2zYs22mESHXPQYzXbB8n4V98jwC, Amount: 13000000 (0.013 with 9 decimals)
	var tokenTransfer *types.Transaction
	for i := range transfers {
		if transfers[i].Type == constant.TxTypeTokenTransfer {
			tokenTransfer = &transfers[i]
			break
		}
	}

	require.NotNil(t, tokenTransfer, "should detect the Token-2022 transferCheckedWithFee from inner instructions")

	assert.Equal(t, txSig, tokenTransfer.TxHash, "TxHash should match")
	assert.Equal(t, "13000000", tokenTransfer.Amount, "Amount should be 13000000")
	assert.Equal(t, "HeLp6NuQkmYB4pYWo2zYs22mESHXPQYzXbB8n4V98jwC", tokenTransfer.AssetAddress, "AssetAddress should be the token mint")
	assert.Equal(t, "BfV1aVZTcN2R8fA9Asa49qsfYzX3rH3jVGkwWAA2muDs", tokenTransfer.FromAddress, "FromAddress should be the source owner")
	assert.Equal(t, "CvySeQ1FR4CgKo4N1L6UhgCaJ8RWJbcXi32ZppCAnRF4", tokenTransfer.ToAddress, "ToAddress should be the destination owner")

	t.Logf("Squads multisig transfer: from=%s to=%s amount=%s token=%s",
		tokenTransfer.FromAddress, tokenTransfer.ToAddress,
		tokenTransfer.Amount, tokenTransfer.AssetAddress)
}
