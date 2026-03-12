package addressbloomfilter

import (
	"context"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"gorm.io/gorm"
)

// WalletAddressBloomFilter defines the interface for working with wallet address filters.
type WalletAddressBloomFilter interface {
	// Initialize fully resets the bloom filter from database state.
	Initialize(ctx context.Context) error

	// InitializeWithTypes resets bloom filters for selected address types only.
	InitializeWithTypes(ctx context.Context, addressTypes []enum.NetworkType) error

	// Add inserts a single address into the bloom filter for a given address type.
	Add(address string, addressType enum.NetworkType)

	// AddBatch inserts multiple addresses into the bloom filter for a given address type.
	AddBatch(addresses []string, addressType enum.NetworkType)

	// Contains checks if a given address exists in the bloom filter for the specified type.
	Contains(address string, addressType enum.NetworkType) bool

	// Clear deletes the bloom filter for a given address type.
	Clear(addressType enum.NetworkType)

	// Stats returns metadata and filter info for the given address type.
	Stats(addressType enum.NetworkType) map[string]any
}

func DefaultAddressTypes() []enum.NetworkType {
	return []enum.NetworkType{
		enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
		enum.NetworkTypeBtc,
		enum.NetworkTypeSol,
		enum.NetworkTypeSui,
	}
}

func NewBloomFilter(
	cfg config.BloomfilterConfig,
	db *gorm.DB,
	redisClient infra.RedisClient,
) WalletAddressBloomFilter {
	walletAddressRepo := repository.NewRepository[model.WalletAddress](db)
	switch cfg.Type {
	case enum.BFBackendRedis:
		return NewRedisBloomFilter(RedisBloomConfig{
			RedisClient:       redisClient,
			WalletAddressRepo: walletAddressRepo,
			BatchSize:         cfg.BatchSize,
			KeyPrefix:         cfg.Redis.KeyPrefix,
			ErrorRate:         cfg.Redis.ErrorRate,
			Capacity:          cfg.Redis.Capacity,
		})
	default:
		return NewAddressBloomFilter(Config{
			WalletAddressRepo: walletAddressRepo,
			ExpectedItems:     cfg.InMemory.ExpectedItems,
			FalsePositiveRate: cfg.InMemory.FalsePositiveRate,
			BatchSize:         cfg.BatchSize,
		})
	}
}
