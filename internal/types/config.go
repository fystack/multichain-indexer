package types

import "time"

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
