package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

// MempoolWorker handles 0-confirmation transaction tracking for Bitcoin
// Polls the mempool for pending transactions and emits them to NATS
type MempoolWorker struct {
	*BaseWorker
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

	transactions, utxoEvents, err := mw.btcIndexer.GetMempoolTransactions(mw.ctx)
	if err != nil {
		mw.logger.Error("Failed to get mempool transactions", "err", err)
		return err
	}

	newTxCount := 0
	newUTXOCount := 0
	networkType := mw.chain.GetNetworkType()

	for _, tx := range transactions {
		toMonitored := tx.ToAddress != "" && mw.pubkeyStore.Exist(networkType, tx.ToAddress)
		fromMonitored := mw.config.TwoWayIndexing && tx.FromAddress != "" && mw.pubkeyStore.Exist(networkType, tx.FromAddress)

		if !toMonitored && !fromMonitored {
			continue
		}

		// seenTxs grows unbounded as the mempool churns. Once it hits 50k entries,
		// evict one random entry before inserting the new one to keep memory bounded.
		// Go map iteration is randomly ordered, so ranging and breaking after the
		// first delete is a lightweight random-eviction without a separate data structure.
		if len(mw.seenTxs) >= 50_000 {
			for k := range mw.seenTxs {
				delete(mw.seenTxs, k)
				break
			}
		}

		candidates := [2]struct {
			active    bool
			direction string
		}{
			{toMonitored, types.DirectionIn},
			{fromMonitored, types.DirectionOut},
		}
		for _, c := range candidates {
			if !c.active {
				continue
			}
			key := tx.TxHash + ":" + c.direction
			if mw.seenTxs[key] {
				continue
			}
			mw.seenTxs[key] = true
			newTxCount++
			dirTx := tx
			dirTx.Direction = c.direction
			if err := mw.emitter.EmitTransaction(mw.chain.GetName(), &dirTx); err != nil {
				mw.logger.Error("Failed to emit mempool transaction",
					"txHash", tx.TxHash, "direction", c.direction, "err", err)
			} else {
				mw.logger.Debug("Emitted mempool transaction",
					"txHash", tx.TxHash, "direction", c.direction,
					"from", tx.FromAddress, "to", tx.ToAddress,
					"amount", tx.Amount, "status", tx.Status)
			}
		}
	}

	if mw.config.IndexUTXO {
		for i := range utxoEvents {
			event := &utxoEvents[i]
			
			if mw.seenTxs[event.TxHash+":utxo"] {
				continue
			}

			isRelevant := false
			for _, utxo := range event.Created {
				if mw.pubkeyStore.Exist(networkType, utxo.Address) {
					isRelevant = true
					break
				}
			}

			if !isRelevant {
				for _, spent := range event.Spent {
					if mw.pubkeyStore.Exist(networkType, spent.Address) {
						isRelevant = true
						break
					}
				}
			}

			if isRelevant {
				mw.seenTxs[event.TxHash+":utxo"] = true
				newUTXOCount++

				if err := mw.emitter.EmitUTXO(mw.chain.GetName(), event); err != nil {
					mw.logger.Error("Failed to emit mempool UTXO",
						"txHash", event.TxHash,
						"err", err,
					)
				} else {
					mw.logger.Debug("Emitted mempool UTXO",
						"txHash", event.TxHash,
						"created", len(event.Created),
						"spent", len(event.Spent),
						"status", event.Status,
					)
				}
			}
		}
	}

	if newTxCount > 0 || newUTXOCount > 0 {
		mw.logger.Info("Processed mempool transactions",
			"new_txs", newTxCount,
			"new_utxos", newUTXOCount,
			"total_tracked", len(mw.seenTxs),
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
