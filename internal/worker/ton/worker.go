package ton

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tonIndexer "github.com/fystack/multichain-indexer/internal/indexer/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/retry"
	"gorm.io/gorm"
)

const walletListKeyFormat = "ton/wallets/%s"

// TonPollingWorker polls multiple TON accounts for transactions.
type TonPollingWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	logger *slog.Logger
	wg     sync.WaitGroup

	chainName   string
	config      config.ChainConfig
	indexer     tonIndexer.AccountIndexer
	cursorStore tonIndexer.CursorStore
	db          *gorm.DB
	kvstore     infra.KVStore
	emitter     events.Emitter

	// Cache
	wallets            []string
	walletSet          map[string]struct{}
	walletsInitialized bool
	walletsMutex       sync.RWMutex

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
	chainName string,
	cfg config.ChainConfig,
	indexer tonIndexer.AccountIndexer,
	cursorStore tonIndexer.CursorStore,
	db *gorm.DB,
	kvstore infra.KVStore,
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
		chainName:    chainName,
		config:       cfg,
		indexer:      indexer,
		cursorStore:  cursorStore,
		db:           db,
		kvstore:      kvstore,
		emitter:      emitter,
		concurrency:  concurrency,
		pollInterval: pollInterval,
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

// pollAllAccounts polls all tracked TON addresses from in-memory cache.
func (w *TonPollingWorker) pollAllAccounts() {
	if err := w.ensureWalletsLoaded(); err != nil {
		w.logger.Error("Failed to ensure wallet list", "err", err)
		return
	}

	addresses := w.snapshotWallets()

	if len(addresses) == 0 {
		w.logger.Debug("No TON addresses to poll")
		return
	}

	w.logger.Info("Starting poll cycle", "address_count", len(addresses))

	workChan := make(chan string, len(addresses))
	for _, addr := range addresses {
		workChan <- addr
	}
	close(workChan)

	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.pollWorker(workChan)
		}()
	}

	wg.Wait()
	w.logger.Debug("Poll cycle complete", "address_count", len(addresses))
}

func (w *TonPollingWorker) snapshotWallets() []string {
	w.walletsMutex.RLock()
	defer w.walletsMutex.RUnlock()
	out := make([]string, len(w.wallets))
	copy(out, w.wallets)
	return out
}

// pollWorker processes addresses from the work channel.
func (w *TonPollingWorker) pollWorker(addresses <-chan string) {
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

	cursor, err := w.cursorStore.Get(w.ctx, address)
	if err != nil {
		log.Error("Failed to get cursor", "err", err)
		return
	}
	if cursor == nil {
		cursor = &tonIndexer.AccountCursor{Address: address}
	}

	if w.ctx.Err() != nil {
		return
	}

	txs, newCursor, err := w.pollAccountWithRetry(address, cursor, log)
	if err != nil {
		log.Error("Failed to poll account after retries", "err", err)
		return
	}
	if w.ctx.Err() != nil {
		return
	}

	if newCursor != nil {
		if err := w.cursorStore.Save(w.ctx, newCursor); err != nil {
			log.Error("Failed to save cursor", "err", err)
		}
	}

	if len(txs) == 0 {
		return
	}

	log.Info("Found transactions", "count", len(txs))
	for i := range txs {
		tx := &txs[i]
		if w.shouldSkipTrackedInternalTransfer(address, tx) {
			log.Debug("Skipping duplicate tracked-wallet transfer from receiver-side poll",
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"txhash", tx.TxHash,
			)
			continue
		}

		log.Info("Emitting matched transaction",
			"type", tx.Type,
			"from", tx.FromAddress,
			"to", tx.ToAddress,
			"asset_address", tx.AssetAddress,
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
}

func (w *TonPollingWorker) pollAccountWithRetry(
	address string,
	cursor *tonIndexer.AccountCursor,
	log *slog.Logger,
) ([]types.Transaction, *tonIndexer.AccountCursor, error) {
	var (
		txs       []types.Transaction
		newCursor *tonIndexer.AccountCursor
	)

	err := retry.Exponential(func() error {
		if w.ctx.Err() != nil {
			return nil
		}

		result, c, pollErr := w.indexer.PollAccount(w.ctx, address, cursor)
		if pollErr != nil {
			if w.ctx.Err() != nil {
				return nil
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
			if w.ctx.Err() == nil {
				log.Debug("Retrying poll", "err", err, "next_retry", next)
			}
		},
	})
	if w.ctx.Err() != nil {
		return nil, nil, nil
	}
	return txs, newCursor, err
}

// AddAddress registers a new address for tracking.
// This initializes the cursor and starts polling the address.
func (w *TonPollingWorker) AddAddress(ctx context.Context, address string) error {
	normalizedAddress := tonIndexer.NormalizeTONAddressRaw(address)
	if normalizedAddress == "" {
		return fmt.Errorf("invalid TON address: %s", address)
	}

	// Check if cursor already exists
	existing, err := w.cursorStore.Get(ctx, normalizedAddress)
	if err != nil {
		return err
	}

	if existing != nil {
		// Already tracking
		return nil
	}

	// Create initial cursor
	cursor := &tonIndexer.AccountCursor{Address: normalizedAddress}
	return w.cursorStore.Save(ctx, cursor)
}

// RemoveAddress stops tracking an address.
func (w *TonPollingWorker) RemoveAddress(ctx context.Context, address string) error {
	normalizedAddress := tonIndexer.NormalizeTONAddressRaw(address)
	if normalizedAddress != "" {
		return w.cursorStore.Delete(ctx, normalizedAddress)
	}
	return w.cursorStore.Delete(ctx, address)
}

// GetNetworkType implements the worker interface.
func (w *TonPollingWorker) GetNetworkType() enum.NetworkType {
	return enum.NetworkTypeTon
}

func (w *TonPollingWorker) GetName() string {
	return w.chainName
}

func (w *TonPollingWorker) walletListKey() string {
	return fmt.Sprintf(walletListKeyFormat, w.chainName)
}

func (w *TonPollingWorker) ensureWalletsLoaded() error {
	w.walletsMutex.RLock()
	initialized := w.walletsInitialized
	w.walletsMutex.RUnlock()

	if initialized {
		return nil
	}

	if _, err := w.ReloadWalletsFromKV(w.ctx); err != nil {
		return err
	}

	w.walletsMutex.RLock()
	hasWallets := len(w.wallets) > 0
	w.walletsMutex.RUnlock()
	if hasWallets {
		return nil
	}

	// KV empty fallback: sync once from DB to bootstrap.
	if w.db == nil {
		return nil
	}
	_, err := w.ReloadWalletsFromDB(w.ctx)
	return err
}

func (w *TonPollingWorker) replaceWalletCache(addresses []string) int {
	walletSet := make(map[string]struct{}, len(addresses))
	for _, addr := range addresses {
		walletSet[addr] = struct{}{}
	}

	w.walletsMutex.Lock()
	oldSize := len(w.wallets)
	w.wallets = addresses
	w.walletSet = walletSet
	w.walletsInitialized = true
	w.walletsMutex.Unlock()

	w.logger.Info("Wallet cache updated",
		"old_size", oldSize,
		"new_size", len(addresses),
	)
	return len(addresses)
}

func (w *TonPollingWorker) isTrackedWallet(address string) bool {
	normalized := tonIndexer.NormalizeTONAddressRaw(address)
	if normalized == "" {
		return false
	}

	w.walletsMutex.RLock()
	_, ok := w.walletSet[normalized]
	w.walletsMutex.RUnlock()
	return ok
}

func (w *TonPollingWorker) shouldSkipTrackedInternalTransfer(polledAddress string, tx *types.Transaction) bool {
	from := tonIndexer.NormalizeTONAddressRaw(tx.FromAddress)
	to := tonIndexer.NormalizeTONAddressRaw(tx.ToAddress)
	if from == "" || to == "" {
		return false
	}

	// Keep only one side when both sender and receiver are tracked.
	// Sender-side poll has polledAddress == from.
	if !w.isTrackedWallet(from) || !w.isTrackedWallet(to) {
		return false
	}

	polled := tonIndexer.NormalizeTONAddressRaw(polledAddress)
	if polled == "" {
		return false
	}

	return polled != from
}

// ReloadWalletsFromKV refreshes in-memory wallets from KV store.
func (w *TonPollingWorker) ReloadWalletsFromKV(_ context.Context) (int, error) {
	if w.kvstore == nil {
		return 0, fmt.Errorf("kvstore is not configured")
	}

	var addresses []string
	found, err := w.kvstore.GetAny(w.walletListKey(), &addresses)
	if err != nil {
		return 0, fmt.Errorf("get wallet list from kv: %w", err)
	}
	if !found {
		w.replaceWalletCache(nil)
		return 0, nil
	}

	return w.replaceWalletCache(tonIndexer.NormalizeTONAddressList(addresses)), nil
}

// ReloadWalletsFromDB reloads wallets from database and persists them to KV store.
func (w *TonPollingWorker) ReloadWalletsFromDB(_ context.Context) (int, error) {
	if w.db == nil {
		return 0, fmt.Errorf("database is not configured")
	}
	if w.kvstore == nil {
		return 0, fmt.Errorf("kvstore is not configured")
	}

	allAddresses, err := w.loadWalletAddressesFromDB()
	if err != nil {
		return 0, err
	}

	cleaned := tonIndexer.NormalizeTONAddressList(allAddresses)
	if err := w.kvstore.SetAny(w.walletListKey(), cleaned); err != nil {
		return 0, fmt.Errorf("persist wallet list to kv: %w", err)
	}

	return w.replaceWalletCache(cleaned), nil
}

// ReloadJettons refreshes TON jetton registry from Redis (with config fallback).
func (w *TonPollingWorker) ReloadJettons(ctx context.Context) (int, error) {
	count, err := w.indexer.ReloadJettons(ctx)
	if err != nil {
		return 0, err
	}

	w.logger.Info("Jetton registry reloaded", "chain", w.chainName, "jetton_count", count)
	return count, nil
}

func (w *TonPollingWorker) loadWalletAddressesFromDB() ([]string, error) {
	const batchSize = 1000

	allAddresses := make([]string, 0, batchSize)
	offset := 0
	for {
		var wallets []model.WalletAddress
		err := w.db.Select("address").
			Where("type = ?", enum.NetworkTypeTon).
			Order("id").
			Limit(batchSize).
			Offset(offset).
			Find(&wallets).Error
		if err != nil {
			return nil, fmt.Errorf("fetch wallets from db (offset=%d): %w", offset, err)
		}
		if len(wallets) == 0 {
			break
		}
		for _, wallet := range wallets {
			allAddresses = append(allAddresses, wallet.Address)
		}
		if len(wallets) < batchSize {
			break
		}
		offset += batchSize
	}

	return allAddresses, nil
}
