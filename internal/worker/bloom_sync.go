package worker

import (
	"context"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
)

// AddressLoader loads addresses for bloom filter synchronization.
// Implementations can query any data source (database, API, file, etc.).
type AddressLoader interface {
	LoadAddresses(ctx context.Context, params AddressLoaderParams) ([]LoadedAddress, error)
}

// AddressLoaderParams contains the parameters for loading addresses.
type AddressLoaderParams struct {
	NetworkType enum.NetworkType
	After       time.Time // Load addresses created after this time
	Limit       int
}

// LoadedAddress represents an address returned by an AddressLoader.
type LoadedAddress struct {
	Address   string
	CreatedAt time.Time
}

// BloomSyncConfig holds configuration for the bloom filter sync worker.
type BloomSyncConfig struct {
	Interval  time.Duration // How often to check for new addresses
	BatchSize int           // Max addresses to fetch per sync cycle
}

// NewBloomSyncConfig creates a BloomSyncConfig from the parsed config,
// applying defaults for any unset fields.
func NewBloomSyncConfig(syncCfg config.BloomSyncConfig) BloomSyncConfig {
	cfg := BloomSyncConfig{
		Interval:  time.Second,
		BatchSize: 500,
	}
	if interval, err := time.ParseDuration(syncCfg.Interval); err == nil {
		cfg.Interval = interval
	}
	if syncCfg.BatchSize > 0 {
		cfg.BatchSize = syncCfg.BatchSize
	}
	return cfg
}

// BloomSyncWorker continuously syncs new addresses to the bloom filter.
// Implements the Worker interface for unified lifecycle management.
type BloomSyncWorker struct {
	ctx    context.Context
	cancel context.CancelFunc

	bloomFilter addressbloomfilter.WalletAddressBloomFilter
	loader      AddressLoader
	config      BloomSyncConfig

	mu              sync.RWMutex
	lastSyncedTimes map[enum.NetworkType]time.Time
	totalSynced     map[enum.NetworkType]uint64
}

// NewBloomSyncWorker creates a new bloom filter sync worker.
func NewBloomSyncWorker(
	ctx context.Context,
	bloomFilter addressbloomfilter.WalletAddressBloomFilter,
	loader AddressLoader,
	config BloomSyncConfig,
) *BloomSyncWorker {
	workerCtx, cancel := context.WithCancel(ctx)
	return &BloomSyncWorker{
		ctx:             workerCtx,
		cancel:          cancel,
		bloomFilter:     bloomFilter,
		loader:          loader,
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
	for _, nt := range enum.AllNetworkTypes {
		w.lastSyncedTimes[nt] = now
		w.totalSynced[nt] = 0
	}
}

func (w *BloomSyncWorker) syncAllNetworks() {
	for _, nt := range enum.AllNetworkTypes {
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

		addresses, err := w.loader.LoadAddresses(w.ctx, AddressLoaderParams{
			NetworkType: networkType,
			After:       queryTime,
			Limit:       w.config.BatchSize,
		})
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
		addressStrings := make([]string, len(addresses))
		for i, a := range addresses {
			addressStrings[i] = a.Address
		}
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
