package retry

import (
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
)

const (
	DefaultMaxAttempts = 3
	DefaultInterval    = 5 * time.Second
)

type Operation func() error

type ExponentialConfig struct {
	InitialInterval time.Duration
	MaxElapsedTime  time.Duration
	OnRetry         func(error, time.Duration)
}

func Exponential(fn Operation, cfg ExponentialConfig) error {
	if cfg.InitialInterval <= 0 {
		return errors.New("initial interval must be > 0")
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = cfg.InitialInterval
	if cfg.MaxElapsedTime > 0 {
		bo.MaxElapsedTime = cfg.MaxElapsedTime
	}

	return backoff.RetryNotify(backoff.Operation(fn), bo, func(err error, next time.Duration) {
		if cfg.OnRetry != nil {
			cfg.OnRetry(err, next)
		}
	})
}

func Constant(fn Operation, interval time.Duration, attempts int) error {
	if attempts <= 0 {
		attempts = 1
	}

	var err error
	for i := 1; i <= attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < attempts {
			time.Sleep(interval)
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", attempts, err)
}
