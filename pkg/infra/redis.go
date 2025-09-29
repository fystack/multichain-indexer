package infra

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/logger"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/stringutils"
	"github.com/redis/go-redis/v9"
)

// RedisClient is a custom interface that abstracts the Redis client methods.
type RedisClient interface {
	GetClient() *redis.Client
	Set(key string, value any, expiration time.Duration) error
	Get(key string) (string, error)
	Del(keys ...string) error

	ZAdd(key string, members ...redis.Z) error
	ZRem(key string, members ...interface{}) error
	ZRange(key string, start, stop int64) ([]string, error)
	ZRangeWithScores(key string, start, stop int64) ([]redis.Z, error)
	ZRevRangeWithScores(key string, start, stop int64) ([]redis.Z, error)

	Close() error
}

// RedisWrapper is a struct that implements the RedisClient interface using a Redis client pointer.
type RedisWrapper struct {
	client *redis.Client
}

func getTlsConfig(
	caCertPath string,
	clientCertPath string,
	clientKeyPath string,
) (*tls.Config, error) {
	// Load the CA cert
	caCert, err := os.ReadFile(stringutils.ExpandTildePath(caCertPath))
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert: %w", err)
	}

	// Create a CA pool and add the cert
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to append CA cert to pool")
	}

	// Load client certificate
	cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	// Create and return the TLS config
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: false, // Ensure proper verification
	}, nil
}

// NewRedisClient creates a new instance of RedisWrapper.
func NewRedisClient(addr string, password string, environment string, mTLS bool) (RedisClient, error) {
	cpus := runtime.GOMAXPROCS(0)
	poolSize := cpus * 10
	minIdle := cpus * 2

	var opts *redis.Options
	var err error

	// Handle redis:// or rediss:// URIs
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		opts, err = redis.ParseURL(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid Redis URL: %w", err)
		}
		// allow password override from config if explicitly set
		if password != "" {
			opts.Password = password
		}
	} else {
		// fallback: plain host:port
		opts = &redis.Options{
			Addr:     addr,
			Password: password,
			DB:       0,
		}
	}

	// Apply default tuning (override only if not already set)
	if opts.PoolSize == 0 {
		opts.PoolSize = poolSize
	}
	if opts.MinIdleConns == 0 {
		opts.MinIdleConns = minIdle
	}
	if opts.ConnMaxLifetime == 0 {
		opts.ConnMaxLifetime = 30 * time.Minute
	}
	if opts.ConnMaxIdleTime == 0 {
		opts.ConnMaxIdleTime = 5 * time.Minute
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 5 * time.Second
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 3 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 3 * time.Second
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	if opts.MinRetryBackoff == 0 {
		opts.MinRetryBackoff = 100 * time.Millisecond
	}
	if opts.MaxRetryBackoff == 0 {
		opts.MaxRetryBackoff = 500 * time.Millisecond
	}

	// Add mTLS if required (for your own deployments, not AWS ElastiCache)
	if environment == constant.EnvProduction && mTLS {
		tlsCfg, err := getTlsConfig(
			"./certs/redis/rootCA.pem",
			"./certs/redis/redis-client.crt",
			"./certs/redis/redis-client.key",
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS config for redis client: %w", err)
		}
		opts.TLSConfig = tlsCfg
	}

	client := redis.NewClient(opts)

	// verify connectivity right away
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if pong, err := client.Ping(ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	} else {
		logger.Info("Connected to Redis", "pong", pong)
	}

	return &RedisWrapper{client: client}, nil
}

// Set implements the RedisClient interface for setting a key-value pair.
func (rw *RedisWrapper) GetClient() *redis.Client {
	return rw.client
}

func (rw *RedisWrapper) Set(key string, value any, expiration time.Duration) error {
	return rw.client.Set(context.Background(), key, value, expiration).Err()
}

// Get implements the RedisClient interface for getting the value by key.
func (rw *RedisWrapper) Get(key string) (string, error) {
	val, err := rw.client.Get(context.Background(), key).Result()
	return val, err
}

func (rw *RedisWrapper) Del(keys ...string) error {
	return rw.client.Del(context.Background(), keys...).Err()
}

func (rw *RedisWrapper) ZAdd(key string, members ...redis.Z) error {
	return rw.client.ZAdd(context.Background(), key, members...).Err()
}

func (rw *RedisWrapper) ZRem(key string, members ...interface{}) error {
	return rw.client.ZRem(context.Background(), key, members...).Err()
}

// ZRange implements the RedisClient interface for getting members in a sorted set.
func (rw *RedisWrapper) ZRange(key string, start, stop int64) ([]string, error) {
	return rw.client.ZRange(context.Background(), key, start, stop).Result()
}

// ZRangeWithScores implements the RedisClient interface for getting members with their scores.
func (rw *RedisWrapper) ZRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return rw.client.ZRangeWithScores(context.Background(), key, start, stop).Result()
}

func (rw *RedisWrapper) ZRevRangeWithScores(key string, start, stop int64) ([]redis.Z, error) {
	return rw.client.ZRevRangeWithScores(context.Background(), key, start, stop).Result()
}

func (rw *RedisWrapper) Close() error {
	return rw.client.Close()
}
