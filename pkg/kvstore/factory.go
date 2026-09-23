package kvstore

import (
	"fmt"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
)

// NewFromConfig constructs an infra.KVStore based on kvstore configuration.
// redisClient is required for the redis backend and ignored otherwise (it may
// be nil when only the badger backend is used).
func NewFromConfig(cfg config.KVSConfig, redisClient *redis.Client) (infra.KVStore, error) {
	switch cfg.Type {
	case enum.KVStoreTypeBadger:
		return NewBadgerStore(cfg.Badger.Directory, cfg.Badger.Prefix, infra.JSON)
	case enum.KVStoreTypeRedis:
		if redisClient == nil {
			return nil, fmt.Errorf("redis kvstore requires a redis client")
		}
		return NewRedisStore(redisClient, cfg.Redis.Prefix, infra.JSON)
	default:
		return nil, fmt.Errorf("unsupported kvstore type: %s", cfg.Type)
	}
}
