package config

import (
	"os"
	"time"

	"github.com/fystack/indexer/internal/types"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Indexer IndexerConfig `yaml:"indexer"`
}

type IndexerConfig struct {
	Chains  map[string]types.ChainConfig `yaml:"chains"`
	NATS    NATSConfig                   `yaml:"nats"`
	Storage StorageConfig                `yaml:"storage"`
}

type NATSConfig struct {
	URL           string `yaml:"url"`
	SubjectPrefix string `yaml:"subject_prefix"`
}

type StorageConfig struct {
	Type string `yaml:"type"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// Set defaults
	for name, chain := range config.Indexer.Chains {
		if chain.BatchSize == 0 {
			chain.BatchSize = 10
		}
		if chain.PollInterval == 0 {
			chain.PollInterval = 3 * time.Second
		}
		config.Indexer.Chains[name] = chain
	}

	return &config, nil
}
