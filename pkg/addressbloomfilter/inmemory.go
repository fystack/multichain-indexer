package addressbloomfilter

import (
	"context"
	"math"
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"github.com/samber/lo"
)

// Config holds dependencies and configuration for the Bloom filter container.
type Config struct {
	WalletAddressRepo repository.Repository[model.WalletAddress] // Repository for loading addresses from DB
	ExpectedItems     uint                                       // Estimated number of addresses per address type
	FalsePositiveRate float64                                    // Desired false positive rate
	BatchSize         int                                        // Batch size for paginated DB fetches
}

type walletBloomFilter struct {
	mu           sync.RWMutex
	filter       *bloom.BloomFilter
	addressCount uint
}

type addressBloomFilter struct {
	mu      sync.RWMutex
	filters map[enum.NetworkType]*walletBloomFilter
	config  Config
}

// NewAddressBloomFilter creates a new singleton bloom filter container using the provided config.
func NewAddressBloomFilter(cfg Config) WalletAddressBloomFilter {
	return &addressBloomFilter{
		filters: make(map[enum.NetworkType]*walletBloomFilter),
		config:  cfg,
	}
}

func (abf *addressBloomFilter) Initialize(ctx context.Context) error {
	return abf.InitializeWithTypes(ctx, DefaultAddressTypes())
}

func (abf *addressBloomFilter) InitializeWithTypes(ctx context.Context, addressTypes []enum.NetworkType) error {
	if len(addressTypes) == 0 {
		logger.Info("Skip in-memory Bloom filter initialization: no address types selected")
		return nil
	}

	seen := make(map[enum.NetworkType]struct{}, len(addressTypes))
	uniqueTypes := make([]enum.NetworkType, 0, len(addressTypes))
	for _, addrType := range addressTypes {
		if _, ok := seen[addrType]; ok {
			continue
		}
		seen[addrType] = struct{}{}
		uniqueTypes = append(uniqueTypes, addrType)
	}

	batchSize := abf.config.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	for _, addrType := range uniqueTypes {
		offset := 0
		limit := batchSize
		total := 0

		for {
			wallets, err := abf.config.WalletAddressRepo.Find(ctx, repository.FindOptions{
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
			abf.AddBatch(addresses, addrType)

			offset += limit
			total += len(addresses)
		}

		logger.Info("In-memory Bloom filter initialized", "addressType", addrType, "total", total)
	}
	return nil
}

func (abf *addressBloomFilter) getOrCreateFilter(addressType enum.NetworkType) *walletBloomFilter {
	abf.mu.Lock()
	defer abf.mu.Unlock()

	if bf, ok := abf.filters[addressType]; ok {
		return bf
	}

	m, k := bloom.EstimateParameters(abf.config.ExpectedItems, abf.config.FalsePositiveRate)
	filter := bloom.New(m, k)

	bf := &walletBloomFilter{
		filter:       filter,
		addressCount: 0,
	}
	abf.filters[addressType] = bf
	return bf
}

func (abf *addressBloomFilter) Add(address string, addressType enum.NetworkType) {
	bf := abf.getOrCreateFilter(addressType)
	bf.mu.Lock()
	defer bf.mu.Unlock()
	bf.filter.Add([]byte(address))
	bf.addressCount++
}

func (abf *addressBloomFilter) AddBatch(addresses []string, addressType enum.NetworkType) {
	bf := abf.getOrCreateFilter(addressType)
	bf.mu.Lock()
	defer bf.mu.Unlock()
	for _, address := range addresses {
		bf.filter.Add([]byte(address))
		bf.addressCount++
	}
}

func (abf *addressBloomFilter) Contains(address string, addressType enum.NetworkType) bool {
	bf := abf.getOrCreateFilter(addressType)
	bf.mu.RLock()
	defer bf.mu.RUnlock()
	return bf.filter.Test([]byte(address))
}

func (abf *addressBloomFilter) Clear(addressType enum.NetworkType) {
	bf := abf.getOrCreateFilter(addressType)
	bf.mu.Lock()
	defer bf.mu.Unlock()
	bf.filter.ClearAll()
	bf.addressCount = 0
}

func (abf *addressBloomFilter) Stats(addressType enum.NetworkType) map[string]any {
	bf := abf.getOrCreateFilter(addressType)
	bf.mu.RLock()
	defer bf.mu.RUnlock()

	fillRatio := bf.approximatedFillRatio()
	return map[string]any{
		"addressType":                addressType,
		"addressCount":               bf.addressCount,
		"bitsCount":                  bf.filter.Cap(),
		"hashFunctions":              bf.filter.K(),
		"approximateFillRatio":       fillRatio,
		"fillPercentage":             fillRatio * 100,
		"estimatedFalsePositiveRate": bf.estimateFalsePositiveRate(),
	}
}

func (bf *walletBloomFilter) approximatedFillRatio() float64 {
	bitset := bf.filter.BitSet()
	bitsSet := bitset.Count()
	totalBits := bitset.Len()
	if totalBits == 0 {
		return 0
	}
	return float64(bitsSet) / float64(totalBits)
}

func (bf *walletBloomFilter) estimateFalsePositiveRate() float64 {
	n := float64(bf.addressCount)
	m := float64(bf.filter.Cap())
	k := float64(bf.filter.K())
	if m == 0 || k == 0 {
		return 0.0
	}
	return math.Pow(1-math.Exp(-k*n/m), k)
}
