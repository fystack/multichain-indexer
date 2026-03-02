package config

import (
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateChainConfig_RequiresNativeDenomForCosmos(t *testing.T) {
	err := validateChainConfig(ChainConfig{
		Type: enum.NetworkTypeCosmos,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "native_denom")
}

func TestValidateChainConfig_AllowsCosmosWithNativeDenom(t *testing.T) {
	err := validateChainConfig(ChainConfig{
		Type:        enum.NetworkTypeCosmos,
		NativeDenom: "uatom",
	})
	require.NoError(t, err)
}

func TestValidateChainConfig_DoesNotRequireNativeDenomForNonCosmos(t *testing.T) {
	err := validateChainConfig(ChainConfig{
		Type: enum.NetworkTypeEVM,
	})
	require.NoError(t, err)
}
