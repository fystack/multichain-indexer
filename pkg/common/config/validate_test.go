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

func TestValidateChainConfig_AllowsDefaultStatusThresholds(t *testing.T) {
	err := validateChainConfig(ChainConfig{
		Type: enum.NetworkTypeEVM,
	})
	require.NoError(t, err)
}

func TestValidateChainConfig_RejectsInvalidStatusThresholds(t *testing.T) {
	err := validateChainConfig(ChainConfig{
		Type: enum.NetworkTypeEVM,
		Status: StatusConfig{
			HealthyMaxPendingBlocks: 300,
			SlowMaxPendingBlocks:    200,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status thresholds invalid")
}

func TestValidateChainConfig_RejectsSlowLowerThanDefaultHealthy(t *testing.T) {
	err := validateChainConfig(ChainConfig{
		Type: enum.NetworkTypeEVM,
		Status: StatusConfig{
			SlowMaxPendingBlocks: 40,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status thresholds invalid")
}
