package rpc

import (
	"net/http"
	"time"

	"github.com/fystack/indexer/internal/pool"
	"github.com/fystack/indexer/internal/ratelimiter"
)

type ClientConfig struct {
	RequestTimeout time.Duration
	RateLimit      RateLimitConfig
	MaxRetries     int
	RetryDelay     time.Duration
}

type RateLimitConfig struct {
	RequestsPerSecond int
	BurstSize         int
}

type HTTPClient struct {
	Pool        *pool.Pool
	RateLimiter *ratelimiter.PooledRateLimiter
	Client      *http.Client
	Config      ClientConfig
}

func NewHTTPClient(nodes []string) *HTTPClient {
	return NewHTTPClientWithConfig(nodes, ClientConfig{
		RequestTimeout: 30 * time.Second,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		MaxRetries: 3,
		RetryDelay: 1 * time.Second,
	})
}

func NewHTTPClientWithConfig(nodes []string, config ClientConfig) *HTTPClient {
	rateDuration := time.Second / time.Duration(config.RateLimit.RequestsPerSecond)

	return &HTTPClient{
		Pool:        pool.New(nodes),
		RateLimiter: ratelimiter.NewPooledRateLimiter(rateDuration, config.RateLimit.BurstSize),
		Client: &http.Client{
			Timeout: config.RequestTimeout,
		},
		Config: config,
	}
}

func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.Client.Do(req)
}

func (c *HTTPClient) GetRateLimitStats() map[string]ratelimiter.Stats {
	return c.RateLimiter.GetStats()
}

func (c *HTTPClient) Close() {
	c.RateLimiter.Close()
}
