package config

import (
	"fmt"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

func validateChainConfig(chain ChainConfig) error {
	if chain.Type == enum.NetworkTypeCosmos && chain.NativeDenom == "" {
		return fmt.Errorf("native_denom is required for cosmos chains")
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
