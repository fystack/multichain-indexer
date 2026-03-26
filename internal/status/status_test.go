package status

import (
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/stretchr/testify/require"
)

func TestRegistrySnapshotDerivesHealthWithPerChainThresholds(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.RegisterChain(
		"ETH_MAINNET",
		"ethereum_mainnet",
		config.ChainConfig{
			NetworkId:    "eth-mainnet",
			InternalCode: "ETH_MAINNET",
			Type:         enum.NetworkTypeEVM,
			Status: config.StatusConfig{
				HealthyMaxPendingBlocks: 20,
				SlowMaxPendingBlocks:    100,
			},
		},
	)

	indexedAt := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	registry.UpdateHead("eth_mainnet", 1_000, 980, indexedAt)
	registry.MarkFailedBlock("eth_mainnet", 981)
	registry.MarkFailedBlock("eth_mainnet", 982)
	registry.SetCatchupRanges("eth_mainnet", []blockstore.CatchupRange{
		{Start: 100, End: 102, Current: 99},
		{Start: 200, End: 201, Current: 199},
	})

	resp := registry.Snapshot("1.2.3")
	require.Equal(t, "1.2.3", resp.Version)
	require.Len(t, resp.Networks, 1)

	network := resp.Networks[0]
	require.Equal(t, "eth-mainnet", network.NetworkID)
	require.Equal(t, "ethereum_mainnet", network.ChainName)
	require.Equal(t, "ETH_MAINNET", network.InternalCode)
	require.Equal(t, "evm", network.NetworkType)
	require.Equal(t, uint64(1_000), network.LatestBlock)
	require.Equal(t, uint64(980), network.IndexedBlock)
	require.Equal(t, uint64(20), network.HeadGap)
	require.Equal(t, uint64(5), network.CatchupPendingBlocks)
	require.Equal(t, uint64(25), network.PendingBlocks)
	require.Equal(t, 2, network.CatchupRanges)
	require.Equal(t, 2, network.FailedBlocks)
	require.Equal(t, HealthSlow, network.Health)
	require.NotNil(t, network.LastIndexedAt)
	require.True(t, network.LastIndexedAt.Equal(indexedAt))
}

func TestRegistrySnapshotUsesDefaultThresholdWhenMissing(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.RegisterChain(
		"TRON_MAINNET",
		"tron_mainnet",
		config.ChainConfig{
			NetworkId:    "tron-mainnet",
			InternalCode: "TRON_MAINNET",
			Type:         enum.NetworkTypeTron,
		},
	)

	registry.UpdateHead("tron_mainnet", 500, 470, time.Time{})

	resp := registry.Snapshot("1.0.0")
	require.Len(t, resp.Networks, 1)

	network := resp.Networks[0]
	require.Equal(t, uint64(30), network.PendingBlocks)
	require.Equal(t, HealthHealthy, network.Health)
}

func TestRegistryClearFailedBlocks(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.RegisterChain("BTC_MAINNET", "bitcoin_mainnet", config.ChainConfig{
		NetworkId:    "btc-mainnet",
		InternalCode: "BTC_MAINNET",
		Type:         enum.NetworkTypeBtc,
	})

	registry.MarkFailedBlock("btc_mainnet", 10)
	registry.MarkFailedBlock("btc_mainnet", 11)
	registry.ClearFailedBlocks("btc_mainnet", []uint64{10})

	resp := registry.Snapshot("1.0.0")
	require.Len(t, resp.Networks, 1)
	require.Equal(t, 1, resp.Networks[0].FailedBlocks)

	registry.SetFailedBlocks("btc_mainnet", []uint64{21, 22, 22})
	resp = registry.Snapshot("1.0.0")
	require.Equal(t, 2, resp.Networks[0].FailedBlocks)
}

func TestRegistryCatchupRangeMutations(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.RegisterChain("SOL_MAINNET", "sol_mainnet", config.ChainConfig{
		NetworkId:    "sol-mainnet",
		InternalCode: "SOL_MAINNET",
		Type:         enum.NetworkTypeSol,
	})

	registry.SetCatchupRanges("sol_mainnet", []blockstore.CatchupRange{
		{Start: 1, End: 10, Current: 0},
		{Start: 20, End: 25, Current: 22},
	})

	resp := registry.Snapshot("1.0.0")
	require.Len(t, resp.Networks, 1)
	require.Equal(t, 2, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(13), resp.Networks[0].CatchupPendingBlocks)

	registry.UpsertCatchupRanges("sol_mainnet", []blockstore.CatchupRange{
		{Start: 1, End: 10, Current: 5},
		{Start: 30, End: 31, Current: 29},
	})

	resp = registry.Snapshot("1.0.0")
	require.Equal(t, 3, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(10), resp.Networks[0].CatchupPendingBlocks)

	registry.DeleteCatchupRange("sol_mainnet", 20, 25)

	resp = registry.Snapshot("1.0.0")
	require.Equal(t, 2, resp.Networks[0].CatchupRanges)
	require.Equal(t, uint64(7), resp.Networks[0].CatchupPendingBlocks)
}
