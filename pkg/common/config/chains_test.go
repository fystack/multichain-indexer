package config

import (
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_MergesStatusThresholds(t *testing.T) {
	t.Parallel()

	chains := Chains{
		"ethereum_mainnet": {
			Type:         enum.NetworkTypeEVM,
			PollInterval: time.Second,
			Nodes:        []NodeConfig{{URL: "https://example.com"}},
			Status: StatusConfig{
				SlowMaxPendingBlocks: 120,
			},
		},
	}

	err := chains.ApplyDefaults(Defaults{
		PollInterval:        time.Second,
		ReorgRollbackWindow: 20,
		Status: StatusConfig{
			HealthyMaxPendingBlocks: 30,
			SlowMaxPendingBlocks:    250,
		},
	})
	require.NoError(t, err)

	chain := chains["ethereum_mainnet"]
	require.Equal(t, uint64(30), chain.Status.HealthyMaxPendingBlocks)
	require.Equal(t, uint64(120), chain.Status.SlowMaxPendingBlocks)
}

func TestApplyDefaults_AppliesEnabledDefaultWhenOmitted(t *testing.T) {
	t.Parallel()

	enabled := false
	chains := Chains{
		"ethereum_mainnet": {
			Type:         enum.NetworkTypeEVM,
			PollInterval: time.Second,
			Nodes:        []NodeConfig{{URL: "https://example.com"}},
		},
	}

	err := chains.ApplyDefaults(Defaults{
		Enabled:             &enabled,
		PollInterval:        time.Second,
		ReorgRollbackWindow: 20,
	})
	require.NoError(t, err)

	require.NotNil(t, chains["ethereum_mainnet"].Enabled)
	require.False(t, *chains["ethereum_mainnet"].Enabled)
}

func TestApplyDefaults_PreservesExplicitEnabledValue(t *testing.T) {
	t.Parallel()

	defaultEnabled := false
	chainEnabled := true
	chains := Chains{
		"ethereum_mainnet": {
			Enabled:      &chainEnabled,
			Type:         enum.NetworkTypeEVM,
			PollInterval: time.Second,
			Nodes:        []NodeConfig{{URL: "https://example.com"}},
		},
	}

	err := chains.ApplyDefaults(Defaults{
		Enabled:             &defaultEnabled,
		PollInterval:        time.Second,
		ReorgRollbackWindow: 20,
	})
	require.NoError(t, err)

	require.NotNil(t, chains["ethereum_mainnet"].Enabled)
	require.True(t, *chains["ethereum_mainnet"].Enabled)
}

func TestEnabledNames_DefaultsToEnabledWhenOmitted(t *testing.T) {
	t.Parallel()

	disabled := false
	chains := Chains{
		"ethereum_mainnet": {
			Type:  enum.NetworkTypeEVM,
			Nodes: []NodeConfig{{URL: "https://example.com"}},
		},
		"tron_mainnet": {
			Enabled: &disabled,
			Type:    enum.NetworkTypeTron,
			Nodes:   []NodeConfig{{URL: "https://example.com"}},
		},
	}

	require.ElementsMatch(t, []string{"ethereum_mainnet"}, chains.EnabledNames())
}

func TestEnabledNames_IncludesExplicitlyEnabledChains(t *testing.T) {
	t.Parallel()

	enabled := true
	disabled := false
	chains := Chains{
		"ethereum_mainnet": {
			Enabled: &enabled,
			Type:    enum.NetworkTypeEVM,
			Nodes:   []NodeConfig{{URL: "https://example.com"}},
		},
		"tron_mainnet": {
			Enabled: &disabled,
			Type:    enum.NetworkTypeTron,
			Nodes:   []NodeConfig{{URL: "https://example.com"}},
		},
	}

	require.ElementsMatch(t, []string{"ethereum_mainnet"}, chains.EnabledNames())
}
