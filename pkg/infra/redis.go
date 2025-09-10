package infra

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/logger"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/stringutils"
	"github.com/redis/go-redis/v9"
)

// RedisClient is a custom interface that abstracts the Redis client methods.
type RedisClient interface {
	GetClient() *redis.Client
	Set(key string, value any, expiration time.Duration) error
	Get(key string) (string, error)
	Del(keys ...string) error
	Close() error
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
func NewRedisClient(addr string, password string, environment string) (RedisClient, error) {
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

func (rw *RedisWrapper) Close() error {
	return rw.client.Close()
}
