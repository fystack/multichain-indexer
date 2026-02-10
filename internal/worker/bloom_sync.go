package worker

import (
	"context"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// BloomSyncConfig holds configuration for the bloom filter sync worker.
type BloomSyncConfig struct {
	Interval  time.Duration // How often to check for new addresses
	BatchSize int           // Max addresses to fetch per sync cycle
}

// DefaultBloomSyncConfig returns sensible defaults for production.
func DefaultBloomSyncConfig() BloomSyncConfig {
	return BloomSyncConfig{
		Interval:  time.Second,
		BatchSize: 500,
	}
}

// BloomSyncWorker continuously syncs new addresses from the database to the bloom filter.
// Implements the Worker interface for unified lifecycle management.
type BloomSyncWorker struct {
	ctx    context.Context
	cancel context.CancelFunc

	bloomFilter addressbloomfilter.WalletAddressBloomFilter
	db          *gorm.DB
	config      BloomSyncConfig

	mu              sync.RWMutex
	lastSyncedTimes map[enum.NetworkType]time.Time
	totalSynced     map[enum.NetworkType]uint64
}

// NewBloomSyncWorker creates a new bloom filter sync worker.
func NewBloomSyncWorker(
	ctx context.Context,
	bloomFilter addressbloomfilter.WalletAddressBloomFilter,
	db *gorm.DB,
	config BloomSyncConfig,
) *BloomSyncWorker {
	workerCtx, cancel := context.WithCancel(ctx)
	return &BloomSyncWorker{
		ctx:             workerCtx,
		cancel:          cancel,
		bloomFilter:     bloomFilter,
		db:              db,
		config:          config,
		lastSyncedTimes: make(map[enum.NetworkType]time.Time),
		totalSynced:     make(map[enum.NetworkType]uint64),
	}
}

// Start begins the background sync loop.
func (w *BloomSyncWorker) Start() {
	go w.run()
}

// Stop gracefully stops the sync worker.
func (w *BloomSyncWorker) Stop() {
	w.cancel()
	logger.Info("Bloom filter sync worker stopped")
}

func (w *BloomSyncWorker) run() {
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()

	w.initLastSyncedTimes()

	logger.Info("Bloom filter sync worker started",
		"interval", w.config.Interval,
		"batchSize", w.config.BatchSize,
	)

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.syncAllNetworks()
		}
	}
}

func (w *BloomSyncWorker) initLastSyncedTimes() {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	for _, nt := range []enum.NetworkType{
		enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
		enum.NetworkTypeBtc,
		enum.NetworkTypeSui,
	} {
		w.lastSyncedTimes[nt] = now
		w.totalSynced[nt] = 0
	}
}

func (w *BloomSyncWorker) syncAllNetworks() {
	for _, nt := range []enum.NetworkType{
		enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
		enum.NetworkTypeBtc,
		enum.NetworkTypeSui,
	} {
		if err := w.syncNetwork(nt); err != nil {
			logger.Error("Bloom sync failed for network",
				"networkType", nt,
				"error", err,
			)
		}
	}
}

// syncNetwork fetches and adds new addresses for a specific network type.
// Loops until fully caught up to handle burst inserts efficiently.
func (w *BloomSyncWorker) syncNetwork(networkType enum.NetworkType) error {
	totalSynced := 0

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		default:
		}

		w.mu.RLock()
		lastSynced := w.lastSyncedTimes[networkType]
		w.mu.RUnlock()

		// Use a small overlap to handle clock skew edge cases
		queryTime := lastSynced.Add(-time.Second)

		var addresses []model.WalletAddress
		err := w.db.WithContext(w.ctx).
			Model(&model.WalletAddress{}).
			Select("address", "created_at").
			Where("type = ? AND created_at > ?", networkType, queryTime).
			Order("created_at ASC").
			Limit(w.config.BatchSize).
			Find(&addresses).Error

		if err != nil {
			return err
		}

		if len(addresses) == 0 {
			if totalSynced > 0 {
				logger.Info("Bloom sync caught up",
					"networkType", networkType,
					"totalSynced", totalSynced,
				)
			}
			return nil
		}

		// Add to bloom filter (idempotent)
		addressStrings := lo.Map(addresses, func(a model.WalletAddress, _ int) string {
			return a.Address
		})
		w.bloomFilter.AddBatch(addressStrings, networkType)

		latestTime := addresses[len(addresses)-1].CreatedAt

		w.mu.Lock()
		w.lastSyncedTimes[networkType] = latestTime
		w.totalSynced[networkType] += uint64(len(addresses))
		w.mu.Unlock()

		totalSynced += len(addresses)

		// If we got less than a full batch, we're caught up
		if len(addresses) < w.config.BatchSize {
			if totalSynced > 0 {
				logger.Debug("Bloom sync completed",
					"networkType", networkType,
					"synced", totalSynced,
				)
			}
			return nil
		}

		logger.Debug("Bloom sync progress",
			"networkType", networkType,
			"batch", len(addresses),
			"totalSynced", totalSynced,
		)
	}
}

// Stats returns sync worker statistics.
func (w *BloomSyncWorker) Stats() map[string]any {
	w.mu.RLock()
	defer w.mu.RUnlock()

	stats := make(map[string]any)
	for nt, count := range w.totalSynced {
		stats[string(nt)] = map[string]any{
			"totalSynced":  count,
			"lastSyncedAt": w.lastSyncedTimes[nt],
		}
	}
	return stats
}
