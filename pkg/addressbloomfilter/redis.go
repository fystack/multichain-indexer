package addressbloomfilter

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/model"
	"github.com/fystack/transaction-indexer/pkg/repository"
	"github.com/samber/lo"
)

type redisBloomFilter struct {
	mu                sync.RWMutex // Add mutex for thread safety
	redisClient       infra.RedisClient
	walletAddressRepo repository.Repository[model.WalletAddress]
	batchSize         int
	keyPrefix         string
	ctx               context.Context
	errorRate         float64
	capacity          int
}

type RedisBloomConfig struct {
	RedisClient       infra.RedisClient
	WalletAddressRepo repository.Repository[model.WalletAddress]
	BatchSize         int
	KeyPrefix         string
	ErrorRate         float64
	Capacity          int
}

func NewRedisBloomFilter(cfg RedisBloomConfig) WalletAddressBloomFilter {
	keyPrefix := cfg.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "bloom_filter"
	}
	errorRate := cfg.ErrorRate
	if errorRate <= 0 {
		errorRate = 0.01
	}
	capacity := cfg.Capacity
	if capacity <= 0 {
		capacity = 10000
	}

	return &redisBloomFilter{
		redisClient:       cfg.RedisClient,
		walletAddressRepo: cfg.WalletAddressRepo,
		batchSize:         cfg.BatchSize,
		keyPrefix:         keyPrefix,
		ctx:               context.Background(),
		errorRate:         errorRate,
		capacity:          capacity,
	}
}

func (rbf *redisBloomFilter) getKey(addressType enum.NetworkType) string {
	return fmt.Sprintf("%s:%s", rbf.keyPrefix, addressType)
}

func (rbf *redisBloomFilter) Initialize(ctx context.Context) error {
	rbf.mu.Lock()
	defer rbf.mu.Unlock()

	types := []enum.NetworkType{
		enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
	}

	for _, addrType := range types {
		key := rbf.getKey(addrType)
		client := rbf.redisClient.GetClient()

		// Check if the Bloom filter key exists, and delete it if so
		exists, err := client.Do(ctx, "EXISTS", key).Int()
		if err != nil {
			return fmt.Errorf("failed to check existence of key %s: %w", key, err)
		}
		if exists == 1 {
			_ = rbf.redisClient.Del(key)
		}

		// Reserve a new Bloom filter
		_, err = client.Do(ctx, "BF.RESERVE", key, rbf.errorRate, rbf.capacity).Result()
		if err != nil {
			return fmt.Errorf("failed to create Bloom filter for %s: %w", addrType, err)
		}

		if rbf.walletAddressRepo == nil {
			return errors.New("WalletAddressRepo was not provided in config")
		}

		offset := 0
		limit := rbf.batchSize
		total := 0

		for {
			wallets, err := rbf.walletAddressRepo.Find(ctx, repository.FindOptions{
				Where:  repository.WhereType{"type": addrType},
				Select: []string{"address"},
				Limit:  uint(limit),
				Offset: uint(offset),
			})
			if err != nil {
				return err
			}
			if len(wallets) == 0 {
				break
			}

			addresses := lo.Map(wallets, func(w *model.WalletAddress, _ int) string {
				return w.Address
			})

			logger.Info("Processing addresses", "addressType", addrType, "count", len(addresses))

			if err := rbf.addBatchToBloom(ctx, key, addresses); err != nil {
				return err
			}

			offset += limit
			total += len(addresses)
		}

		logger.Info("Redis Bloom filter initialized", "addressType", addrType, "total", total)
	}

	return nil
}

func (rbf *redisBloomFilter) addBatchToBloom(
	ctx context.Context,
	key string,
	addresses []string,
) error {
	client := rbf.redisClient.GetClient()
	args := make([]any, 0, len(addresses)+2)
	args = append(args, "BF.MADD", key)
	for _, addr := range addresses {
		args = append(args, addr)
	}
	_, err := client.Do(ctx, args...).Result()
	return err
}

func (rbf *redisBloomFilter) Add(address string, addressType enum.NetworkType) {
	rbf.mu.Lock()
	defer rbf.mu.Unlock()

	key := rbf.getKey(addressType)
	client := rbf.redisClient.GetClient()

	_, err := client.Do(rbf.ctx, "BF.ADD", key, address).Result()
	if err != nil {
		logger.Error("Failed to add address to Redis bloom filter", "error", err)
	}
}

func (rbf *redisBloomFilter) AddBatch(addresses []string, addressType enum.NetworkType) {
	if len(addresses) == 0 {
		return
	}
	rbf.mu.Lock()
	defer rbf.mu.Unlock()

	key := rbf.getKey(addressType)
	if err := rbf.addBatchToBloom(rbf.ctx, key, addresses); err != nil {
		logger.Error("Failed to add batch to Redis bloom filter", "error", err)
	}
}

func (rbf *redisBloomFilter) Contains(address string, addressType enum.NetworkType) bool {
	rbf.mu.RLock()
	defer rbf.mu.RUnlock()

	key := rbf.getKey(addressType)
	client := rbf.redisClient.GetClient()

	result, err := client.Do(rbf.ctx, "BF.EXISTS", key, address).Bool()
	if err != nil {
		logger.Error("Error checking Redis bloom filter", "error", err)
		return false
	}
	return result
}

func (rbf *redisBloomFilter) Clear(addressType enum.NetworkType) {
	rbf.mu.Lock()
	defer rbf.mu.Unlock()

	key := rbf.getKey(addressType)
	_ = rbf.redisClient.Del(key)
}

func (rbf *redisBloomFilter) Stats(addressType enum.NetworkType) map[string]any {
	rbf.mu.RLock()
	defer rbf.mu.RUnlock()

	logger.Info("Redis Bloom filter not supported yet")
	return nil
}
