package worker

import (
	"context"
	"fmt"
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
	seenTxs       map[string]bool // Track seen transactions to avoid duplicates
	pollInterval  time.Duration   // How often to poll mempool
	btcIndexer    *indexer.BitcoinIndexer
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

	// Get mempool transactions
	transactions, err := mw.btcIndexer.GetMempoolTransactions(mw.ctx)
	if err != nil {
		mw.logger.Error("Failed to get mempool transactions", "err", err)
		return err
	}

	// Track new transactions
	newTxCount := 0
	for _, tx := range transactions {
		// Skip if we've already seen this transaction
		if mw.seenTxs[tx.TxHash] {
			continue
		}

		// Mark as seen
		mw.seenTxs[tx.TxHash] = true
		newTxCount++

		// Emit transaction to NATS (only if ToAddress is monitored)
		if mw.pubkeyStore.Exist(mw.chain.GetNetworkType(), tx.ToAddress) {
			if err := mw.emitter.EmitTransaction(mw.chain.GetName(), &tx); err != nil {
				mw.logger.Error("Failed to emit mempool transaction",
					"txHash", tx.TxHash,
					"err", err,
				)
			} else {
				mw.logger.Debug("Emitted mempool transaction",
					"txHash", tx.TxHash,
					"to", tx.ToAddress,
					"amount", tx.Amount,
					"confirmations", tx.Confirmations,
					"status", tx.Status,
				)
			}
		}
	}

	if newTxCount > 0 {
		mw.logger.Info("Processed mempool transactions",
			"new_txs", newTxCount,
			"total_tracked", len(mw.seenTxs),
		)
	}

	// Cleanup: Remove old transactions from tracking (keep last 10k)
	if len(mw.seenTxs) > 10000 {
		// Clear half the map (simple approach)
		count := 0
		for txHash := range mw.seenTxs {
			delete(mw.seenTxs, txHash)
			count++
			if count > 5000 {
				break
			}
		}
		mw.logger.Debug("Cleaned up old mempool transactions", "removed", count)
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
