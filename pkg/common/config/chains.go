package config

import (
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/goccy/go-yaml"
)

type ChainsConfig struct {
	Defaults ChainConfig            `yaml:"defaults" validate:"-"`
	Items    map[string]ChainConfig `yaml:",inline" validate:"required,dive,keys,required,endkeys,required"`
}

// UnmarshalYAML splits out "defaults" from inline chain entries
func (c *ChainsConfig) UnmarshalYAML(b []byte) error {
	var raw map[string]ChainConfig
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		raw = map[string]ChainConfig{}
	}
	if def, ok := raw["defaults"]; ok {
		c.Defaults = def
		delete(raw, "defaults")
	} else {
		c.Defaults = ChainConfig{}
	}
	c.Items = raw
	return nil
}

type ChainConfig struct {
	Name                string         `yaml:"name" validate:"required"`
	Type                enum.ChainType `yaml:"type" validate:"required,oneof=tron evm"`
	Nodes               []Node         `yaml:"nodes" validate:"required,min=1,dive"`
	StartBlock          uint64         `yaml:"start_block"`
	FromLatest          bool           `yaml:"from_latest"`
	ReorgRollbackWindow int            `yaml:"reorg_rollback_window"`
	BatchSize           int            `yaml:"batch_size" validate:"gt=0"`
	PollInterval        time.Duration  `yaml:"poll_interval"`
	Client              ClientCfg      `yaml:"client"`
}

type ClientCfg struct {
	Timeout    time.Duration `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries"`
	RetryDelay time.Duration `yaml:"retry_delay"`
	Throttle   ThrottleCfg   `yaml:"throttle"`
}

type ThrottleCfg struct {
	RPS   int `yaml:"rps"`
	Burst int `yaml:"burst"`
}

func (c *ChainsConfig) GetAllChainNames() []string {
	names := make([]string, 0, len(c.Items))
	for name := range c.Items {
		names = append(names, name)
	}
	return names
}

func (c *ChainsConfig) ValidateChains(chains []string) error {
	for _, chain := range chains {
		if _, ok := c.Items[chain]; !ok {
			return fmt.Errorf("chain %s not found", chain)
		}
	}
	return nil
}

func (c *ChainsConfig) OverrideFromLatest(chains []string) {
	for _, chain := range chains {
		if cc, ok := c.Items[chain]; ok {
			cc.FromLatest = true
			c.Items[chain] = cc
		}
	}
}

func (c *ChainsConfig) GetChain(chain string) (ChainConfig, error) {
	if cc, ok := c.Items[chain]; ok {
		return cc, nil
	}
	return ChainConfig{}, fmt.Errorf("chain %s not found", chain)
}
