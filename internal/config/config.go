package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type ChainConfig struct {
	Name         string          `yaml:"name"`
	Nodes        []string        `yaml:"nodes"`
	StartBlock   int64           `yaml:"start_block"`
	BatchSize    int             `yaml:"batch_size"`
	PollInterval time.Duration   `yaml:"poll_interval"`
	RateLimit    RateLimitConfig `yaml:"rate_limit"`
	Client       ClientConfig    `yaml:"client"`
}

type RateLimitConfig struct {
	RequestsPerSecond int `yaml:"requests_per_second"`
	BurstSize         int `yaml:"burst_size"`
}

type ClientConfig struct {
	RequestTimeout time.Duration `yaml:"request_timeout"`
	MaxRetries     int           `yaml:"max_retries"`
	RetryDelay     time.Duration `yaml:"retry_delay"`
}

type Config struct {
	Indexer IndexerConfig `yaml:"indexer"`
}

type IndexerConfig struct {
	Chains  map[string]ChainConfig `yaml:"chains"`
	NATS    NATSConfig             `yaml:"nats"`
	Storage StorageConfig          `yaml:"storage"`
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
