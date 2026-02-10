package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

// MempoolWorker handles 0-confirmation transaction tracking for Bitcoin
// Polls the mempool for pending transactions and emits them to NATS
type MempoolWorker struct {
	*BaseWorker
	mu           sync.Mutex
	seenTxs      map[string]bool // Track seen transactions to avoid duplicates
	pollInterval time.Duration   // How often to poll mempool
	btcIndexer   *indexer.BitcoinIndexer
}

// NewMempoolWorker creates a new mempool worker for Bitcoin chains
func NewMempoolWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *MempoolWorker {
	worker := newWorkerWithMode(
		ctx,
		chain,
		cfg,
		kv,
		blockStore,
		emitter,
		pubkeyStore,
		ModeMempool,
		failedChan,
	)

	// Cast to Bitcoin indexer (mempool is Bitcoin-specific)
	btcIndexer, ok := chain.(*indexer.BitcoinIndexer)
	if !ok {
		panic(fmt.Sprintf("MempoolWorker requires BitcoinIndexer, got %T", chain))
	}

	// Use configured poll interval, or default to 30 seconds
	pollInterval := cfg.PollInterval
	if pollInterval == 0 {
		pollInterval = 30 * time.Second
	}

	return &MempoolWorker{
		BaseWorker:   worker,
		seenTxs:      make(map[string]bool),
		pollInterval: pollInterval,
		btcIndexer:   btcIndexer,
	}
}

// Start begins the mempool polling loop
func (mw *MempoolWorker) Start() {
	mw.logger.Info("Starting mempool worker",
		"chain", mw.chain.GetName(),
		"poll_interval", mw.pollInterval,
	)
	go mw.run(mw.processMempool)
}

// Stop stops the mempool worker
func (mw *MempoolWorker) Stop() {
	mw.logger.Info("Stopping mempool worker", "chain", mw.chain.GetName())
	mw.BaseWorker.Stop()
}

// processMempool polls the mempool for new transactions
func (mw *MempoolWorker) processMempool() error {
	mw.logger.Debug("Polling mempool", "chain", mw.chain.GetName())

	// Get mempool transactions (already filtered by monitored addresses in indexer)
	transactions, err := mw.btcIndexer.GetMempoolTransactions(mw.ctx)
	if err != nil {
		mw.logger.Error("Failed to get mempool transactions", "err", err)
		return err
	}

	// Track and emit new transactions (only TO monitored addresses - deposits)
	newTxCount := 0
	networkType := mw.chain.GetNetworkType()

	mw.mu.Lock()
	for _, tx := range transactions {
		// Only emit transactions where TO address is monitored (incoming deposits)
		// Outgoing transactions are handled by the withdrawal flow
		toMonitored := tx.ToAddress != "" && mw.pubkeyStore.Exist(networkType, tx.ToAddress)
		if !toMonitored {
			continue
		}

		// Create unique key for deduplication (txHash + toAddress for UTXO model)
		txKey := tx.TxHash + ":" + tx.ToAddress
		if mw.seenTxs[txKey] {
			continue
		}

		// Mark as seen
		mw.seenTxs[txKey] = true
		newTxCount++

		// Emit transaction to NATS
		if err := mw.emitter.EmitTransaction(mw.chain.GetName(), &tx); err != nil {
			mw.logger.Error("Failed to emit mempool transaction",
				"txHash", tx.TxHash,
				"direction", "incoming",
				"err", err,
			)
		} else {
			mw.logger.Debug("Emitted mempool transaction",
				"txHash", tx.TxHash,
				"direction", "incoming",
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"amount", tx.Amount,
				"status", tx.Status,
			)
		}
	}

	trackedCount := len(mw.seenTxs)

	// Cleanup: Remove old transactions from tracking (keep last 10k)
	if trackedCount > 10000 {
		count := 0
		for txKey := range mw.seenTxs {
			delete(mw.seenTxs, txKey)
			count++
			if count > 5000 {
				break
			}
		}
		mw.logger.Debug("Cleaned up old mempool transactions", "removed", count)
	}
	mw.mu.Unlock()

	if newTxCount > 0 {
		mw.logger.Info("Processed mempool transactions",
			"new_txs", newTxCount,
			"total_tracked", trackedCount,
		)
	}

	// Sleep until next poll interval
	select {
	case <-mw.ctx.Done():
		return mw.ctx.Err()
	case <-time.After(mw.pollInterval):
		// Continue to next iteration
	}

	return nil
}
