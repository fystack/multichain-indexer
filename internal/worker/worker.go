package worker

import (
	"context"
	"strings"
	"time"

	"log/slog"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
)

type WorkerMode string

const (
	ModeRegular   WorkerMode = "regular"
	ModeCatchup   WorkerMode = "catchup"
	ModeRescanner WorkerMode = "rescanner"
	ModeManual    WorkerMode = "manual"
)

type Worker interface {
	Start()
	Stop()
}

// BaseWorker is the common structure for any worker type
type BaseWorker struct {
	config       config.ChainConfig
	chain        indexer.Indexer
	kvstore      infra.KVStore
	blockStore   *blockstore.Store
	emitter      *events.Emitter
	currentBlock uint64
	ctx          context.Context
	cancel       context.CancelFunc
	pubkeyStore  pubkeystore.Store
	mode         WorkerMode
	failedChan   chan FailedBlockEvent
	logger       *slog.Logger
}

// Stop stops the worker and cleans up resources
func (bw *BaseWorker) Stop() {
	if bw.currentBlock > 0 {
		_ = bw.blockStore.SaveLatestBlock(bw.chain.GetName(), bw.currentBlock)
	}
	bw.cancel()
	if bw.kvstore != nil {
		_ = bw.kvstore.Close()
		bw.kvstore = nil
	}
	if bw.emitter != nil {
		bw.emitter.Close()
		bw.emitter = nil
	}
	if bw.blockStore != nil {
		_ = bw.blockStore.Close()
		bw.blockStore = nil
	}
	bw.logger.Info("Worker stopped", "chain", bw.chain.GetName())
}

// run executes the given job repeatedly based on PollInterval
func (bw *BaseWorker) run(job func() error) {
	ticker := time.NewTicker(bw.config.PollInterval)
	defer ticker.Stop()

	errorCount := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-bw.ctx.Done():
			bw.logger.Info("Context done, exiting run loop")
			return
		case <-ticker.C:
			start := time.Now()
			if err := job(); err != nil {
				errorCount++
				bw.logger.Error("Error processing blocks",
					"chain", bw.chain.GetName(),
					"error", err,
					"consecutive_errors", errorCount)
				_ = bw.emitter.EmitError(bw.chain.GetName(), err)

				if errorCount >= maxConsecutiveErrors {
					bw.logger.Warn("Too many consecutive errors, sleeping to prevent overload",
						"chain", bw.chain.GetName())
					time.Sleep(bw.config.PollInterval)
					errorCount = 0
				}
			} else {
				errorCount = 0
			}
			// Ensure PollInterval between job starts
			elapsed := time.Since(start)
			if elapsed < bw.config.PollInterval {
				time.Sleep(bw.config.PollInterval - elapsed)
			}
		}
	}
}

// emitBlock emits transactions related to addresses in the Bloom filter
func (bw *BaseWorker) emitBlock(block *types.Block) {
	if bw.pubkeyStore == nil || block == nil {
		return
	}

	addressType := bw.chain.GetAddressType()
	for _, tx := range block.Transactions {
		matched := bw.pubkeyStore.Exist(addressType, tx.ToAddress) ||
			bw.pubkeyStore.Exist(addressType, tx.FromAddress)

		if matched {
			bw.logger.Info("Emitting transaction",
				"chain", bw.chain.GetName(),
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"addressType", addressType,
			)
			_ = bw.emitter.EmitTransaction(bw.chain.GetName(), &tx)
		}
	}
}

// handleBlockResult processes a single block, returns true if success
func (bw *BaseWorker) handleBlockResult(result indexer.BlockResult) bool {
	if result.Error != nil {
		_ = bw.blockStore.SaveFailedBlock(bw.chain.GetName(), result.Number)
		// Non-blocking send to failedChan
		select {
		case bw.failedChan <- FailedBlockEvent{
			Chain:   bw.chain.GetName(),
			Block:   result.Number,
			Attempt: 1,
		}:
		default:
			bw.logger.Warn("failedChan full, skipping push", "block", result.Number)
		}

		bw.logger.Error("Failed block",
			"chain", bw.chain.GetName(),
			"block", result.Number,
			"error", result.Error.Message,
		)
		return false
	}

	if result.Block == nil {
		bw.logger.Error("Nil block", "chain", bw.chain.GetName(), "block", result.Number)
		return false
	}

	// Update currentBlock on success
	bw.currentBlock = result.Block.Number

	// Persist block hash for reorg detection
	_ = bw.blockStore.SaveBlockHash(bw.chain.GetName(), result.Block.Number, result.Block.Hash)

	// Emit transactions if relevant
	bw.emitBlock(result.Block)

	bw.logger.Info("Handled block successfully", "chain", bw.chain.GetName(), "number", result.Block.Number)
	return true
}

// newWorkerWithMode initializes a BaseWorker with mode and logging
func newWorkerWithMode(ctx context.Context, chain indexer.Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *blockstore.Store, emitter *events.Emitter, pubkeyStore pubkeystore.Store, mode WorkerMode, failedChan chan FailedBlockEvent) *BaseWorker {
	ctx, cancel := context.WithCancel(ctx)
	logger := logger.With(slog.String("mode", strings.ToUpper(string("["+string(mode)+"]"))))

	return &BaseWorker{
		chain:       chain,
		config:      config,
		kvstore:     kv,
		blockStore:  blockStore,
		emitter:     emitter,
		pubkeyStore: pubkeyStore,
		ctx:         ctx,
		cancel:      cancel,
		mode:        mode,
		logger:      logger,
		failedChan:  failedChan,
	}
}
