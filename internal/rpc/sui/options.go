package sui

import (
	"time"
)

type clientOptions struct {
	timeout     time.Duration
	maxMsgSize  int
	keepalive   time.Duration
	retryPolicy *string
}

// Option configures client behavior
type Option func(*clientOptions)

// WithTimeout sets the default request timeout
func WithTimeout(timeout time.Duration) Option {
	return func(o *clientOptions) {
		o.timeout = timeout
	}
}

// WithMaxMessageSize sets the maximum message size
func WithMaxMessageSize(size int) Option {
	return func(o *clientOptions) {
		o.maxMsgSize = size
	}
}

// WithKeepalive sets keepalive interval
func WithKeepalive(interval time.Duration) Option {
	return func(o *clientOptions) {
		o.keepalive = interval
	}
}

// WithRetry enables automatic retry with exponential backoff
func WithRetry() Option {
	return func(o *clientOptions) {
		policy := defaultRetryPolicy()
		o.retryPolicy = policy
	}
}

func defaultRetryPolicy() *string {
	policy := defaultRetryPolicyString()
	return &policy
}

func defaultRetryPolicyString() string {
	return `{
		"methodConfig": [{
			"name": [{"service": "sui.rpc.v2"}],
			"retryPolicy": {
				"maxAttempts": 3,
				"initialBackoff": "0.5s",
				"maxBackoff": "5s",
				"backoffMultiplier": 2.0,
				"retryableStatusCodes": ["UNAVAILABLE", "RESOURCE_EXHAUSTED"]
			}
		}]
	}`
}
