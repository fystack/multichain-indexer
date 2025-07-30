package chains

import (
	"time"

	"github.com/fystack/indexer/internal/config"
)

const (
	ChainTron = "tron"
	ChainEVM  = "evm"
)

var DefaultChainConfig = config.ChainConfig{
	RateLimit: config.RateLimitConfig{
		RequestsPerSecond: 10,
		BurstSize:         20,
	},
	Client: config.ClientConfig{
		RequestTimeout: 30 * time.Second,
		MaxRetries:     3,
		RetryDelay:     1 * time.Second,
	},
}
