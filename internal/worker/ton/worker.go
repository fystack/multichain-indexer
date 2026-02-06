package ton

import (
	"context"
	"log/slog"
	"sync"
	"time"

	tonIndexer "github.com/fystack/multichain-indexer/internal/indexer/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/retry"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
	"gorm.io/gorm"
)

// TonPollingWorker polls multiple TON accounts for transactions.
type TonPollingWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	logger *slog.Logger
	wg     sync.WaitGroup

	config      config.ChainConfig
	indexer     tonIndexer.AccountIndexer
	cursorStore tonIndexer.CursorStore
	pubkeyStore pubkeystore.Store
	db          *gorm.DB
	emitter     events.Emitter

	// Cache
	wallets      []string
	walletsMutex sync.RWMutex
	lastSyncTime time.Time
	syncInterval time.Duration

	// Worker configuration
	concurrency  int
	pollInterval time.Duration
}

// WorkerConfig holds configuration for the TON polling worker.
type WorkerConfig struct {
	Concurrency  int           // Max parallel account polls (default: 10)
	PollInterval time.Duration // Interval between poll cycles (default: from chain config)
}

// NewTonPollingWorker creates a new TON polling worker.
func NewTonPollingWorker(
	ctx context.Context,
	cfg config.ChainConfig,
	indexer tonIndexer.AccountIndexer,
	cursorStore tonIndexer.CursorStore,
	pubkeyStore pubkeystore.Store,
	db *gorm.DB,
	emitter events.Emitter,
	workerCfg WorkerConfig,
) *TonPollingWorker {
	ctx, cancel := context.WithCancel(ctx)

	log := logger.With(
		slog.String("worker", "ton-polling"),
		slog.String("chain", cfg.Name),
	)

	concurrency := workerCfg.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}

	pollInterval := workerCfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = cfg.PollInterval
	}

	return &TonPollingWorker{
		ctx:          ctx,
		cancel:       cancel,
		logger:       log,
		config:       cfg,
		indexer:      indexer,
		cursorStore:  cursorStore,
		pubkeyStore:  pubkeyStore,
		db:           db,
		emitter:      emitter,
		concurrency:  concurrency,
		pollInterval: pollInterval,
		syncInterval: time.Minute, // Sync from DB every minute
	}
}

// Start begins the polling loop.
func (w *TonPollingWorker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop gracefully stops the worker.
func (w *TonPollingWorker) Stop() {
	w.cancel()
	w.wg.Wait()
	w.logger.Info("TON polling worker stopped")
}

// run is the main polling loop.
func (w *TonPollingWorker) run() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.logger.Info("TON polling worker started",
		"poll_interval", w.pollInterval,
		"concurrency", w.concurrency,
	)

	// Run immediately on start
	w.pollAllAccounts()

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("Context cancelled, stopping polling loop")
			return

		case <-ticker.C:
			w.pollAllAccounts()
		}
	}
}

// pollAllAccounts polls all tracked TON addresses from the database.
func (w *TonPollingWorker) pollAllAccounts() {
	// Sync addresses from DB if needed
	w.syncWalletsIfNeeded()

	w.walletsMutex.RLock()
	addresses := make([]string, len(w.wallets))
	copy(addresses, w.wallets)
	w.walletsMutex.RUnlock()

	if len(addresses) == 0 {
		// Only log periodically if needed, or debug
		w.logger.Debug("No TON addresses to poll")
		return
	}

	w.logger.Info("Starting poll cycle", "address_count", len(addresses))

	// Create work channel
	workChan := make(chan string, len(addresses))
	for _, addr := range addresses {
		workChan <- addr
	}
	close(workChan)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			w.pollWorker(workerID, workChan)
		}(i)
	}

	wg.Wait()
	w.logger.Debug("Poll cycle complete", "address_count", len(addresses))
}

// pollWorker processes addresses from the work channel.
func (w *TonPollingWorker) pollWorker(workerID int, addresses <-chan string) {
	for addr := range addresses {
		select {
		case <-w.ctx.Done():
			return
		default:
			w.pollAccount(addr)
		}
	}
}

// pollAccount polls a single account with retry logic.
func (w *TonPollingWorker) pollAccount(address string) {
	log := w.logger.With("address", address)

	// Get existing cursor
	cursor, err := w.cursorStore.Get(w.ctx, address)
	if err != nil {
		log.Error("Failed to get cursor", "err", err)
		return
	}

	// If no cursor exists, initialize one
	if cursor == nil {
		cursor = &tonIndexer.AccountCursor{Address: address}
	}

	// Check context before starting
	if w.ctx.Err() != nil {
		return
	}

	// Poll with retry
	var txs []types.Transaction
	var newCursor *tonIndexer.AccountCursor

	err = retry.Exponential(func() error {
		// Check context cancellation - don't retry on shutdown
		if w.ctx.Err() != nil {
			return nil // Return nil to exit retry loop cleanly
		}

		result, c, pollErr := w.indexer.PollAccount(w.ctx, address, cursor)
		if pollErr != nil {
			// Check if error is due to context cancellation
			if w.ctx.Err() != nil {
				return nil // Exit retry cleanly on shutdown
			}
			return pollErr
		}
		txs = result
		newCursor = c
		return nil
	}, retry.ExponentialConfig{
		InitialInterval: 2 * time.Second,
		MaxElapsedTime:  30 * time.Second,
		OnRetry: func(err error, next time.Duration) {
			// Only log if context is still active
			if w.ctx.Err() == nil {
				log.Debug("Retrying poll", "err", err, "next_retry", next)
			}
		},
	})

	// Exit if context was cancelled
	if w.ctx.Err() != nil {
		return
	}

	if err != nil {
		log.Error("Failed to poll account after retries", "err", err)
		return
	}

	// Process transactions
	if len(txs) == 0 {
		return
	}

	log.Info("Found transactions", "count", len(txs))

	// Emit each transaction to NATS
	for i := range txs {
		tx := &txs[i]
		log.Info("Emitting matched transaction",
			"type", tx.Type,
			"from", tx.FromAddress,
			"to", tx.ToAddress,
			"amount", tx.Amount,
			"fee", tx.TxFee.String(),
			"txhash", tx.TxHash,
		)
		if err := w.emitter.EmitTransaction(w.config.InternalCode, tx); err != nil {
			log.Error("Failed to emit transaction", "tx_hash", tx.TxHash, "err", err)
		} else {
			log.Debug("Emitted transaction", "tx_hash", tx.TxHash)
		}
	}

	// Update cursor after successful processing
	if newCursor != nil {
		if err := w.cursorStore.Save(w.ctx, newCursor); err != nil {
			log.Error("Failed to save cursor", "err", err)
		}
	}
}

// AddAddress registers a new address for tracking.
// This initializes the cursor and starts polling the address.
func (w *TonPollingWorker) AddAddress(ctx context.Context, address string) error {
	// Check if cursor already exists
	existing, err := w.cursorStore.Get(ctx, address)
	if err != nil {
		return err
	}

	if existing != nil {
		// Already tracking
		return nil
	}

	// Create initial cursor
	cursor := &tonIndexer.AccountCursor{Address: address}
	return w.cursorStore.Save(ctx, cursor)
}

// RemoveAddress stops tracking an address.
func (w *TonPollingWorker) RemoveAddress(ctx context.Context, address string) error {
	return w.cursorStore.Delete(ctx, address)
}

// GetNetworkType implements the worker interface.
func (w *TonPollingWorker) GetNetworkType() enum.NetworkType {
	return enum.NetworkTypeTon
}

// syncWalletsIfNeeded refreshes the wallet list from DB if sync interval passed.
// Uses pagination to efficiently handle large numbers of wallets (>10K).
// Before fetching everything, it checks the count to avoid redundant work.
func (w *TonPollingWorker) syncWalletsIfNeeded() {
	if time.Since(w.lastSyncTime) < w.syncInterval {
		return
	}

	// 1. Quickly check the count in DB
	var dbCount int64
	err := w.db.Model(&model.WalletAddress{}).
		Where("type = ?", enum.NetworkTypeTon).
		Count(&dbCount).Error
	if err != nil {
		w.logger.Error("Failed to count wallets in DB", "err", err)
		return
	}

	w.walletsMutex.RLock()
	cachedCount := int64(len(w.wallets))
	w.walletsMutex.RUnlock()

	// If count matches and we're not empty, skip the expensive full fetch
	if dbCount == cachedCount && cachedCount > 0 {
		w.lastSyncTime = time.Now()
		return
	}

	// 2. Count changed or list is empty, perform full sync
	const batchSize = 1000
	var allAddresses []string
	offset := 0

	for {
		var wallets []model.WalletAddress
		// Paginated query for TON wallets only selecting address field
		err := w.db.Select("address").
			Where("type = ?", enum.NetworkTypeTon).
			Order("id").
			Limit(batchSize).
			Offset(offset).
			Find(&wallets).Error

		if err != nil {
			w.logger.Error("Failed to fetch wallets from DB", "err", err, "offset", offset)
			return
		}

		// No more results
		if len(wallets) == 0 {
			break
		}

		// Collect addresses
		for _, wallet := range wallets {
			allAddresses = append(allAddresses, wallet.Address)
		}

		// If we got fewer than batchSize, we're done
		if len(wallets) < batchSize {
			break
		}

		offset += batchSize
	}

	w.walletsMutex.Lock()
	oldSize := len(w.wallets)
	w.wallets = allAddresses
	w.lastSyncTime = time.Now()
	w.walletsMutex.Unlock()

	w.logger.Info("Synced wallets from DB",
		"old_size", oldSize,
		"new_size", len(allAddresses),
	)
}
