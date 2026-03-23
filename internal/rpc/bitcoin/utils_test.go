package bitcoin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeBTCAddress_P2TR_Mainnet verifies that a P2TR mainnet address
// (witness v1, bech32m) is accepted and returned unchanged (Bug #4).
func TestNormalizeBTCAddress_P2TR_Mainnet(t *testing.T) {
	addr := "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr"
	got, err := NormalizeBTCAddress(addr)
	require.NoError(t, err)
	assert.Equal(t, addr, got)
}

// TestNormalizeBTCAddress_P2TR_Testnet verifies that a P2TR testnet address
// (witness v1, bech32m) is accepted and returned unchanged (Bug #4).
func TestNormalizeBTCAddress_P2TR_Testnet(t *testing.T) {
	addr := "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"
	got, err := NormalizeBTCAddress(addr)
	require.NoError(t, err)
	assert.Equal(t, addr, got)
}

// TestNormalizeBTCAddress_P2TR_NormalizesUppercase verifies that an uppercase
// P2TR address is lowercased and returned without error (Bug #4).
func TestNormalizeBTCAddress_P2TR_NormalizesUppercase(t *testing.T) {
	upper := "BC1P5CYXNUXMEUWUVKWFEM96LQZSZD02N6XDCJRS20CAC6YQJJWUDPXQKEDRCR"
	got, err := NormalizeBTCAddress(upper)
	require.NoError(t, err)
	assert.Equal(t, "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr", got)
}

// TestNormalizeBTCAddress_P2WPKH_StillWorks verifies that a P2WPKH (witness v0,
// bech32) address is still accepted after the P2TR fast-path was added.
func TestNormalizeBTCAddress_P2WPKH_StillWorks(t *testing.T) {
	addr := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	got, err := NormalizeBTCAddress(addr)
	require.NoError(t, err)
	assert.Equal(t, addr, got)
}

// TestNormalizeBTCAddress_P2PKH_StillWorks verifies that a P2PKH (legacy base58)
// address is still accepted and returned unchanged.
// Uses 12higDjoCCNXSA95xZMWUdPvXNmkAduhWv — a well-known valid mainnet P2PKH address.
func TestNormalizeBTCAddress_P2PKH_StillWorks(t *testing.T) {
	addr := "12higDjoCCNXSA95xZMWUdPvXNmkAduhWv"
	got, err := NormalizeBTCAddress(addr)
	require.NoError(t, err)
	assert.Equal(t, addr, got)
}

// TestNormalizeBTCAddress_Empty_ReturnsError verifies that an empty string
// returns an error.
func TestNormalizeBTCAddress_Empty_ReturnsError(t *testing.T) {
	_, err := NormalizeBTCAddress("")
	require.Error(t, err)
}
