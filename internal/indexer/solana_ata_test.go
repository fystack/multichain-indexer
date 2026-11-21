package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Owner -> USDC token account pairs from a devnet transaction's token balances (Nov 2026).
func TestSolanaAssociatedTokenAddress_MatchesChain(t *testing.T) {
	const devnetUSDC = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	for owner, want := range map[string]string{
		"8QSZu4pQpzSaxZjB9nYTT6c6xC4yMKTPacga9evirFna": "5K5ivASYeQzKSNwHjraiJRwjFF37431ENKwm38tfPDny",
		"7KeRLcDr25jadfVqhzaEpZ1VFhgteEqSHEoDq94hoEjs": "6axzNyb7KSRzixzEU2eZx9bjENLFcoWrCdNRLJNrnNCf",
		"BvTTJStPnzX9t6wwwTmk2dMyBT5D3sUyBZhUR5MLxzkL": "93Ux13n4QiSor8giJZGrpbtA7UKJuVqUGSwY7CNDrNip",
		"8DtnCvwvi7T6XcBWssccBACWYKtcFVUyF4xbDK8Noznm": "HjVu1Cxtv7nBVyN4h662ooMmfFJXsrnqshb2YPy4u5Tz",
	} {
		got, err := SolanaAssociatedTokenAddress(owner, devnetUSDC, solanaTokenProgramID)
		require.NoError(t, err)
		assert.Equal(t, want, got, owner)
	}
}

func TestSolanaAssociatedTokenAddress_RejectsInvalidKeys(t *testing.T) {
	_, err := SolanaAssociatedTokenAddress("not-a-key", "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU", solanaTokenProgramID)
	assert.Error(t, err)
}
