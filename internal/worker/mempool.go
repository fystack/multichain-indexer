//go:build legacyworkers
// +build legacyworkers

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

// MempoolWorker handles 0-confirmation transaction tracking for Bitcoin.
type MempoolWorker struct {
	*BaseWorker
	seenTxs      map[string]bool
	pollInterval time.Duration
	btcIndexer   *indexer.BitcoinIndexer
}

// NewMempoolWorker creates a new mempool worker for Bitcoin chains.
func NewMempoolWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	_ infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	redisClient infra.RedisClient,
) *MempoolWorker {
	deps := WorkerDeps{
		Chain:        chain,
		Config:       cfg,
		BlockStore:   blockStore,
		PubkeyStore:  pubkeyStore,
		Emitter:      emitter,
		FailureQueue: NewRedisFailureQueue(redisClient),
		Redis:        redisClient,
	}

	worker := newWorkerWithMode(ctx, deps, ModeMempool)

	btcIndexer, ok := chain.(*indexer.BitcoinIndexer)
	if !ok {
		panic(fmt.Sprintf("MempoolWorker requires BitcoinIndexer, got %T", chain))
	}

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

func (mw *MempoolWorker) Start() {
	mw.logger.Info("Starting mempool worker",
		"chain", mw.deps.Chain.GetName(),
		"poll_interval", mw.pollInterval,
	)
	go mw.run(mw.processMempool)
}

func (mw *MempoolWorker) Stop() {
	mw.logger.Info("Stopping mempool worker", "chain", mw.deps.Chain.GetName())
	mw.BaseWorker.Stop()
}

func (mw *MempoolWorker) processMempool() error {
	transactions, err := mw.btcIndexer.GetMempoolTransactions(mw.ctx)
	if err != nil {
		mw.logger.Error("Failed to get mempool transactions", "err", err)
		return err
	}

	newTxCount := 0
	networkType := mw.deps.Chain.GetNetworkType()

	for _, tx := range transactions {
		toMonitored := tx.ToAddress != "" && mw.deps.PubkeyStore.Exist(networkType, tx.ToAddress)
		if !toMonitored {
			continue
		}

		txKey := tx.TxHash + ":" + tx.ToAddress
		if mw.seenTxs[txKey] {
			continue
		}

		mw.seenTxs[txKey] = true
		newTxCount++

		if err := mw.deps.Emitter.EmitTransaction(mw.deps.Chain.GetName(), &tx); err != nil {
			mw.logger.Error("Failed to emit mempool transaction", "txHash", tx.TxHash, "err", err)
		}
	}

	if newTxCount > 0 {
		mw.logger.Info("Processed mempool transactions", "new_txs", newTxCount, "total_tracked", len(mw.seenTxs))
	}

	if len(mw.seenTxs) > 10000 {
		count := 0
		for txKey := range mw.seenTxs {
			delete(mw.seenTxs, txKey)
			count++
			if count > 5000 {
				break
			}
		}
	}

	select {
	case <-mw.ctx.Done():
		return mw.ctx.Err()
	case <-time.After(mw.pollInterval):
	}

	return nil
}
