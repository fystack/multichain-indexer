package config

import (
	"fmt"

	"dario.cat/mergo"
)

// GetChain returns a chain config by name.
func (c Chains) GetChain(name string) (ChainConfig, error) {
	chain, ok := c[name]
	if !ok {
		return ChainConfig{}, fmt.Errorf("chain not found: %s", name)
	}
	return chain, nil
}

// Names returns all chain names.
func (c Chains) Names() []string {
	names := make([]string, 0, len(c))
	for k := range c {
		names = append(names, k)
	}
	return names
}

// Validate checks if given chain names exist in config.
func (c Chains) Validate(names []string) error {
	for _, name := range names {
		if _, ok := c[name]; !ok {
			return fmt.Errorf("chain not found in config: %s", name)
		}
	}
	return nil
}

// OverrideFromLatest sets FromLatest = true for given chains.
func (c Chains) OverrideFromLatest(names []string) {
	for _, name := range names {
		if chain, ok := c[name]; ok {
			chain.FromLatest = true
			c[name] = chain
		}
	}
}

// ApplyDefaults merges global defaults into all chain configs.
func (c Chains) ApplyDefaults(def Defaults) error {
	for name, chain := range c {
		if chain.PollInterval == 0 {
			chain.PollInterval = def.PollInterval
		}
		if chain.ReorgRollbackWindow == 0 {
			chain.ReorgRollbackWindow = def.ReorgRollbackWindow
		}
		if err := mergo.Merge(&chain.Client, def.Client); err != nil {
			return fmt.Errorf("merge client defaults for %s: %w", name, err)
		}
		if err := mergo.Merge(&chain.Throttle, def.Throttle); err != nil {
			return fmt.Errorf("merge throttle defaults for %s: %w", name, err)
		}
		c[name] = chain
	}
	return nil
}
