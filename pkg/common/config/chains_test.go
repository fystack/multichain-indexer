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
