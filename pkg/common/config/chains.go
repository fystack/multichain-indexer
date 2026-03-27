package config

import (
	"fmt"
	"strings"

	"dario.cat/mergo"
)

// CanonicalChainKey is the normalized chain identifier used as registry keys, indexer GetName(),
// and ChainConfig.Name after Load (uppercase + trim). Callers should pass raw YAML keys or CLI names.
func CanonicalChainKey(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

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

// EnabledNames returns chain names whose enabled flag is either true or omitted.
func (c Chains) EnabledNames() []string {
	names := make([]string, 0, len(c))
	for k, chain := range c {
		if chain.Enabled == nil || *chain.Enabled {
			names = append(names, k)
		}
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
		if strings.TrimSpace(chain.NetworkId) == "" {
			chain.NetworkId = name
		}
		if strings.TrimSpace(chain.InternalCode) == "" {
			chain.InternalCode = strings.ToUpper(name)
		}
		if chain.Enabled == nil && def.Enabled != nil {
			enabled := *def.Enabled
			chain.Enabled = &enabled
		}
		if !chain.FromLatest {
			chain.FromLatest = def.FromLatest
		}
		if !chain.TwoWayIndexing {
			chain.TwoWayIndexing = def.TwoWayIndexing
		}
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
		if err := mergo.Merge(&chain.Status, def.Status); err != nil {
			return fmt.Errorf("merge status defaults for %s: %w", name, err)
		}
		chain.Status = chain.Status.Normalize()
		c[name] = chain
	}
	return nil
}
