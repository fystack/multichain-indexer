package kvstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
)

const redisOpTimeout = 5 * time.Second

// RedisStore implements infra.KVStore on top of a Redis client.
type RedisStore struct {
	client *redis.Client
	prefix string
	codec  infra.Codec
}

func NewRedisStore(client *redis.Client, prefix string, codec infra.Codec) (*RedisStore, error) {
	if client == nil {
		return nil, errors.New("redis client is nil")
	}
	if codec == nil {
		codec = infra.JSON
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}
	return &RedisStore{client: client, prefix: prefix, codec: codec}, nil
}

func (r *RedisStore) fullKey(k string) string {
	if r.prefix != "" {
		return r.prefix + "/" + k
	}
	return k
}

func ctxTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), redisOpTimeout)
}

func (r *RedisStore) GetName() string {
	return string(enum.KVStoreTypeRedis)
}

func (r *RedisStore) Set(k string, v string) error {
	if err := checkKeyAndValue(k, v); err != nil {
		return err
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	return r.client.Set(ctx, r.fullKey(k), v, 0).Err()
}

func (r *RedisStore) Get(k string) (string, error) {
	if k == "" {
		return "", ErrKeyEmpty
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	v, err := r.client.Get(ctx, r.fullKey(k)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrKeyNotFound
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

func (r *RedisStore) SetAny(k string, v any) error {
	if err := checkKeyAndValue(k, v); err != nil {
		return err
	}
	data, err := r.codec.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	return r.client.Set(ctx, r.fullKey(k), data, 0).Err()
}

func (r *RedisStore) GetAny(k string, v any) (bool, error) {
	if err := checkKeyAndValue(k, v); err != nil {
		return false, err
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	data, err := r.client.Get(ctx, r.fullKey(k)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, r.codec.Unmarshal(data, v)
}

// List returns all key-value pairs whose key starts with prefix. It uses SCAN
// (non-blocking, cursor-based) rather than KEYS to avoid stalling Redis.
func (r *RedisStore) List(prefix string) ([]*infra.KVPair, error) {
	if prefix == "" {
		return nil, errors.New("prefix is empty")
	}
	ctx, cancel := ctxTimeout()
	defer cancel()

	match := r.fullKey(prefix) + "*"
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	var cursor uint64
	for {
		batch, next, err := r.client.Scan(ctx, cursor, match, 100).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range batch {
			if _, ok := seen[k]; ok {
				// SCAN may return duplicate keys across cursor iterations.
				continue
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}

	values, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make([]*infra.KVPair, 0, len(keys))
	for i, k := range keys {
		raw := values[i]
		if raw == nil {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		result = append(result, &infra.KVPair{Key: k, Value: []byte(s)})
	}
	return result, nil
}

// BatchSet writes multiple key-value pairs in a single pipeline. Unlike Consul's
// transaction API this is not atomic, which is acceptable for the idempotent
// state (catchup ranges) written through it.
func (r *RedisStore) BatchSet(pairs []infra.KVPair) error {
	if len(pairs) == 0 {
		return nil
	}
	ctx, cancel := ctxTimeout()
	defer cancel()

	pipe := r.client.Pipeline()
	for _, p := range pairs {
		pipe.Set(ctx, r.fullKey(p.Key), p.Value, 0)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis batch set failed: %w", err)
	}
	return nil
}

func (r *RedisStore) Delete(k string) error {
	if k == "" {
		return ErrKeyEmpty
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	return r.client.Del(ctx, r.fullKey(k)).Err()
}

// Close is a no-op: the underlying Redis client is shared and closed by its owner.
func (r *RedisStore) Close() error {
	return nil
}
