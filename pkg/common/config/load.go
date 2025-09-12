package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/goccy/go-yaml"
)

var validate = validator.New()

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// apply defaults
	if err := cfg.Chains.ApplyDefaults(cfg.Defaults); err != nil {
		return nil, err
	}

	// validate
	if err := validate.Struct(&cfg); err != nil {
		return nil, fmt.Errorf("struct validation failed: %w", err)
	}

	for name, chain := range cfg.Chains {
		// apply name to struct name
		chain.Name = strings.ToUpper(name)
		if err := validate.Struct(chain); err != nil {
			return nil, fmt.Errorf("chain %s validation failed: %w", name, err)
		}
		cfg.Chains[name] = chain
	}

	return &cfg, nil
}
