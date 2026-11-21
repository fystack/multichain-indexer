package config

import (
	"fmt"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

func validateChainConfig(chain ChainConfig) error {
	if chain.Type == enum.NetworkTypeCosmos && chain.NativeDenom == "" {
		return fmt.Errorf("native_denom is required for cosmos chains")
	}
	if !chain.IsFromLatest() && chain.StartBlock == 0 {
		return fmt.Errorf("start_block is required when from_latest is false")
	}
	switch chain.Solana.WatchMode {
	case "", "blocks":
	case SolanaWatchAccounts:
		if chain.Type != enum.NetworkTypeSol {
			return fmt.Errorf("solana.watch_mode is only for sol chains")
		}
	default:
		return fmt.Errorf("solana.watch_mode must be blocks or accounts, not %q", chain.Solana.WatchMode)
	}
	statusCfg := chain.Status.Normalize()
	if statusCfg.HealthyMaxPendingBlocks >= statusCfg.SlowMaxPendingBlocks {
		return fmt.Errorf(
			"status thresholds invalid: healthy_max_pending_blocks (%d) must be less than slow_max_pending_blocks (%d)",
			statusCfg.HealthyMaxPendingBlocks,
			statusCfg.SlowMaxPendingBlocks,
		)
	}
	return nil
}
