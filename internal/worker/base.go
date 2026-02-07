package worker

import (
	"context"
	"strings"
	"time"

	"log/slog"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/retry"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

// BaseWorker holds the common state and logic shared by all worker types.
type BaseWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	mode   WorkerMode
	logger *slog.Logger

	config      config.ChainConfig
	chain       indexer.Indexer
	kvstore     infra.KVStore
	blockStore  blockstore.Store
	pubkeyStore pubkeystore.Store
	emitter     events.Emitter
	failedChan  chan FailedBlockEvent
}

// Stop stops the worker and cleans up internal resources
func (bw *BaseWorker) Stop() {
	bw.cancel()
	bw.logger.Info("Worker stopped", "chain", bw.chain.GetName())
}

// newWorkerWithMode constructs a BaseWorker with the given mode and logger.
func newWorkerWithMode(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	mode WorkerMode,
	failedChan chan FailedBlockEvent,
) *BaseWorker {
	ctx, cancel := context.WithCancel(ctx)
	log := logger.With(
		slog.String("mode", strings.ToUpper(string(mode))),
		slog.String("chain", chain.GetName()),
	)

	return &BaseWorker{
		ctx:         ctx,
		cancel:      cancel,
		mode:        mode,
		logger:      log,
		config:      cfg,
		chain:       chain,
		kvstore:     kv,
		blockStore:  blockStore,
		pubkeyStore: pubkeyStore,
		emitter:     emitter,
		failedChan:  failedChan,
	}
}

// run executes the given job repeatedly at PollInterval with error handling.
func (bw *BaseWorker) run(job func() error) {
	ticker := time.NewTicker(bw.config.PollInterval)
	defer ticker.Stop()

	const retryInterval = 2 * time.Second

	for {
		select {
		case <-bw.ctx.Done():
			bw.logger.Info("Context done, stopping worker loop")
			return

		case <-ticker.C:
			start := time.Now()

			// Use Exponential retry for the job
			if err := retry.Exponential(job, retry.ExponentialConfig{
				InitialInterval: retryInterval,
				MaxElapsedTime:  bw.config.PollInterval * 4,
				OnRetry: func(err error, next time.Duration) {
					bw.logger.Debug("Retrying job",
						"err", err,
						"next_retry_in", next)
				},
			}); err != nil {
				bw.logger.Error("Job error",
					"err", err,
				)
				_ = bw.emitter.EmitError(bw.chain.GetName(), err)
			}

			// Maintain minimum PollInterval between job starts
			if elapsed := time.Since(start); elapsed < bw.config.PollInterval {
				time.Sleep(bw.config.PollInterval - elapsed)
			}
		}
	}
}

// handleBlockResult processes a block result and persists/forwards errors if needed.
func (bw *BaseWorker) handleBlockResult(result indexer.BlockResult) bool {
	if result.Error != nil {
		_ = bw.blockStore.SaveFailedBlock(bw.chain.GetNetworkInternalCode(), result.Number)

		// Non-blocking push to failedChan
		select {
		case bw.failedChan <- FailedBlockEvent{
			Chain:   bw.chain.GetName(),
			Block:   result.Number,
			Attempt: 1,
		}:
		default:
			bw.logger.Warn("failedChan full, dropping block event", "block", result.Number)
		}

		bw.logger.Error("Failed to process block",
			"chain", bw.chain.GetName(),
			"block", result.Number,
			"err", result.Error.Message,
		)
		return false
	}

	if result.Block == nil {
		bw.logger.Error("Nil block result",
			"chain", bw.chain.GetName(),
			"block", result.Number,
		)
		return false
	}

	// Emit transactions if relevant
	bw.emitBlock(result.Block)

	bw.logger.Info("Processed block successfully",
		"chain", bw.chain.GetName(),
		"block", result.Block.Number,
	)
	return true
}

// emitBlock emits relevant transactions for subscribed addresses.
// Only emits transactions where ToAddress is monitored (incoming deposits).
// Outgoing transactions are handled by the transactor/withdrawal flow.
func (bw *BaseWorker) emitBlock(block *types.Block) {
	if block == nil || bw.pubkeyStore == nil {
		return
	}

	addressType := bw.chain.GetNetworkType()
	for _, tx := range block.Transactions {
		// Check if ToAddress is monitored (incoming transfer/deposit)
		toMonitored := tx.ToAddress != "" && bw.pubkeyStore.Exist(addressType, tx.ToAddress)
		// Check if FromAddress is monitored (outgoing transfer/sweep)
		// Note: Withdrawals are also outgoing but initiated differently
		fromMonitored := tx.FromAddress != "" && bw.pubkeyStore.Exist(addressType, tx.FromAddress)

		// Emit incoming event for receiver
		if toMonitored {
			bw.logger.Info("Emitting matched transaction",
				"direction", "incoming",
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"chain", bw.chain.GetName(),
				"addressType", addressType,
				"txhash", tx.TxHash,
				"status", tx.Status,
				"confirmations", tx.Confirmations,
			)
			_ = bw.emitter.EmitTransactionWithKey(bw.chain.GetName(), &tx, tx.TxHash+"-in")
		}

		// Emit outgoing event for sender (enables balance decrements for sweeps)
		// This ensures custody creates transaction records for BOTH sender and receiver
		// which triggers finalization (and balance updates) for both wallets
		if fromMonitored {
			bw.logger.Info("Emitting matched transaction",
				"direction", "outgoing",
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"chain", bw.chain.GetName(),
				"addressType", addressType,
				"txhash", tx.TxHash,
				"status", tx.Status,
				"confirmations", tx.Confirmations,
			)
			_ = bw.emitter.EmitTransactionWithKey(bw.chain.GetName(), &tx, tx.TxHash+"-out")
		}
	}
}
