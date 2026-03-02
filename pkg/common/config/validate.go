package config

import (
	"fmt"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

func validateChainConfig(chain ChainConfig) error {
	if chain.Type == enum.NetworkTypeCosmos && chain.NativeDenom == "" {
		return fmt.Errorf("native_denom is required for cosmos chains")
	}
	return nil
}
