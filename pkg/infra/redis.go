package infra

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	logger "log/slog"
	"os"
	"runtime"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/stringutils"
	"github.com/redis/go-redis/v9"
)

// RedisClient is a custom interface that abstracts the Redis client methods.
type RedisClient interface {
	GetClient() *redis.Client
	Set(key string, value interface{}, expiration time.Duration) error
	Get(key string) (string, error)
	Incr(key string) error
	Del(keys ...string) error
	HSet(key string, values ...interface{}) error
	HGetAll(key string, dest interface{}) error

	ZAdd(key string, members ...redis.Z) error
	ZRem(key string, members ...interface{}) error
	ZRange(key string, start, stop int64) ([]string, error)
	ZRangeWithScores(key string, start, stop int64) ([]redis.Z, error)
	ZRevRangeWithScores(key string, start, stop int64) ([]redis.Z, error)

	SAdd(key string, members ...interface{}) error
	SRem(key string, members ...interface{}) error
	SMembers(key string) ([]string, error)

	AcquireLock(ctx context.Context, key string, expiration time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
	Expire(key string, expiration time.Duration) error
	TTL(key string) (time.Duration, error)
	Exists(key string) (bool, error)
	Close() error

	// Transaction handling
	TxPipeline() redis.Pipeliner
	TxPipelineExec(pipe redis.Pipeliner) error
}

// RedisWrapper is a struct that implements the RedisClient interface using a Redis client pointer.
type RedisWrapper struct {
	client *redis.Client
}

func getTlsConfig(caCertPath string, clientCertPath string, clientKeyPath string) (*tls.Config, error) {
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

// NewRedisWrapper creates a new instance of RedisWrapper.
func NewRedisClient(addr string, password string, environment string) RedisClient {
	// Compute pool size based on CPU
	cpus := runtime.GOMAXPROCS(0)
	poolSize := cpus * 10 // ~10 connections per CPU
	minIdle := cpus * 2   // keep a few always idle

	opts := &redis.Options{
		Addr:            addr,
		Password:        password,
		DB:              0,
		PoolSize:        poolSize,
		MinIdleConns:    minIdle,
		ConnMaxLifetime: 30 * time.Minute, // recycle sockets older than this
		ConnMaxIdleTime: 5 * time.Minute,  // close sockets idle for longer than this
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		// retry up to 3 times before giving up
		MaxRetries:      3,
		MinRetryBackoff: 100 * time.Millisecond,
		MaxRetryBackoff: 500 * time.Millisecond,
	}

	// In production, layer on TLS
	if environment == constant.EnvProduction {
		tlsCfg, err := getTlsConfig(
			"~/.local/share/mkcert/rootCA.pem",
			"./certs/redis/redis-client.crt",
			"./certs/redis/redis-client.key",
		)
		if err != nil {
			logger.Error("Failed to create TLS config for redis client: ", err)
			return nil
		}
		opts.TLSConfig = tlsCfg
	}

	client := redis.NewClient(opts)

	// verify connectivity right away
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if pong, err := client.Ping(ctx).Result(); err != nil {
		logger.Error("Failed to connect to redis", err)
	} else {
		logger.Info("Connected to Redis", "pong", pong)
	}

	return &RedisWrapper{client: client}
}

// Set implements the RedisClient interface for setting a key-value pair.
func (rw *RedisWrapper) GetClient() *redis.Client {
	return rw.client
}

func (rw *RedisWrapper) Set(key string, value interface{}, expiration time.Duration) error {
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

// Get implements the RedisClient interface for getting the value by key.
func (rw *RedisWrapper) HSet(key string, values ...interface{}) error {
	return rw.client.HSet(context.Background(), key, values...).Err()
}

func (rw *RedisWrapper) Incr(key string) error {
	return rw.client.Incr(context.Background(), key).Err()
}

func (rw *RedisWrapper) HGetAll(key string, dest interface{}) error {
	return rw.client.HGetAll(context.Background(), key).Scan(dest)
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

func (rw *RedisWrapper) AcquireLock(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	return rw.client.SetNX(ctx, key, true, expiration).Result()
}

func (rw *RedisWrapper) ReleaseLock(ctx context.Context, key string) error {
	return rw.client.Del(ctx, key).Err()
}

func (rw *RedisWrapper) Expire(key string, expiration time.Duration) error {
	return rw.client.Expire(context.Background(), key, expiration).Err()
}

func (rw *RedisWrapper) SAdd(key string, members ...interface{}) error {
	return rw.client.SAdd(context.Background(), key, members...).Err()
}

func (rw *RedisWrapper) SRem(key string, members ...interface{}) error {
	return rw.client.SRem(context.Background(), key, members...).Err()
}

func (rw *RedisWrapper) SMembers(key string) ([]string, error) {
	return rw.client.SMembers(context.Background(), key).Result()
}

// Implement transaction methods in RedisWrapper
func (rw *RedisWrapper) TxPipeline() redis.Pipeliner {
	return rw.client.TxPipeline()
}

func (rw *RedisWrapper) TxPipelineExec(pipe redis.Pipeliner) error {
	_, err := pipe.Exec(context.Background())
	return err
}

// Add the implementation for TTL
func (rw *RedisWrapper) TTL(key string) (time.Duration, error) {
	return rw.client.TTL(context.Background(), key).Result()
}

// Add the implementation for Exists
func (rw *RedisWrapper) Exists(key string) (bool, error) {
	result, err := rw.client.Exists(context.Background(), key).Result()
	if err != nil {
		return false, err
	}
	return result > 0, nil
}
