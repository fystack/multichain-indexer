package solana

import (
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

// NewClient is a convenience wrapper to match other rpc packages naming style.
func NewClient(baseURL string, auth *rpc.AuthConfig, timeout time.Duration, rl *ratelimiter.PooledRateLimiter) *Client {
	return NewSolanaClient(baseURL, auth, timeout, rl)
}

