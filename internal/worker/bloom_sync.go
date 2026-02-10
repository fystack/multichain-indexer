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
	Interval  time.Duration // How often to check for new addresses (e.g., 1s, 5s)
	BatchSize int           // Max addresses to fetch per sync cycle
}

// DefaultBloomSyncConfig returns sensible defaults for production.
func DefaultBloomSyncConfig() BloomSyncConfig {
	return BloomSyncConfig{
		Interval:  time.Second, // 1 second latency for new addresses
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

	// Track last synced timestamp per network type for incremental sync
	mu              sync.RWMutex
	lastSyncedTimes map[enum.NetworkType]time.Time

	// Metrics
	totalSynced map[enum.NetworkType]uint64
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
// Implements Worker interface.
func (w *BloomSyncWorker) Start() {
	go w.run()
}

// Stop gracefully stops the sync worker.
// Implements Worker interface.
func (w *BloomSyncWorker) Stop() {
	w.cancel()
	logger.Info("Bloom filter sync worker stopped")
}

// run is the main sync loop.
func (w *BloomSyncWorker) run() {
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()

	// Initialize last synced times to now (Initialize() already loaded historical data)
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

// initLastSyncedTimes sets initial sync times to now for all network types.
func (w *BloomSyncWorker) initLastSyncedTimes() {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	networkTypes := []enum.NetworkType{
		enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
		enum.NetworkTypeBtc,
		enum.NetworkTypeSui,
	}

	for _, nt := range networkTypes {
		w.lastSyncedTimes[nt] = now
		w.totalSynced[nt] = 0
	}
}

// syncAllNetworks syncs new addresses for all network types.
func (w *BloomSyncWorker) syncAllNetworks() {
	networkTypes := []enum.NetworkType{
		enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
		enum.NetworkTypeBtc,
		enum.NetworkTypeSui,
	}

	for _, nt := range networkTypes {
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
		// Check context cancellation
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		default:
		}

		w.mu.RLock()
		lastSynced := w.lastSyncedTimes[networkType]
		w.mu.RUnlock()

		// Fetch addresses created after last sync time
		// Use a small overlap (1 second) to handle edge cases with clock skew
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
			// Fully caught up
			if totalSynced > 0 {
				logger.Info("Bloom sync caught up",
					"networkType", networkType,
					"totalSynced", totalSynced,
				)
			}
			return nil
		}

		// Add to bloom filter (idempotent - adding same address twice is safe)
		addressStrings := lo.Map(addresses, func(a model.WalletAddress, _ int) string {
			return a.Address
		})
		w.bloomFilter.AddBatch(addressStrings, networkType)

		// Update last synced time to the latest address's created_at
		latestTime := addresses[len(addresses)-1].CreatedAt

		w.mu.Lock()
		w.lastSyncedTimes[networkType] = latestTime
		w.totalSynced[networkType] += uint64(len(addresses))
		w.mu.Unlock()

		totalSynced += len(addresses)

		// If we got a full batch, there might be more - continue looping
		if len(addresses) < w.config.BatchSize {
			// Less than batch size means we're caught up
			if totalSynced > 0 {
				logger.Debug("Bloom sync completed",
					"networkType", networkType,
					"synced", totalSynced,
				)
			}
			return nil
		}

		// Log progress for large syncs
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
