package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fystack/indexer/internal/chains"
	"github.com/fystack/indexer/internal/events"
	"github.com/fystack/indexer/internal/types"
)

type Worker struct {
	chain        chains.ChainIndexer
	config       types.ChainConfig
	emitter      *events.Emitter
	currentBlock int64
	ctx          context.Context
	cancel       context.CancelFunc
}

func NewWorker(chain chains.ChainIndexer, config types.ChainConfig, emitter *events.Emitter) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{
		chain:        chain,
		config:       config,
		emitter:      emitter,
		currentBlock: config.StartBlock,
		ctx:          ctx,
		cancel:       cancel,
	}
}

func (w *Worker) Start() {
	go w.run()
}

func (w *Worker) Stop() {
	w.cancel()
}

func (w *Worker) run() {
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	slog.Info("Starting indexer for chain", "chain", w.chain.GetName())

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			if err := w.processBlocks(); err != nil {
				slog.Error("Error processing blocks for chain", "chain", w.chain.GetName(), "error", err)
				if err := w.emitter.EmitError(w.chain.GetName(), err); err != nil {
					slog.Error("Failed to emit error event", "chain", w.chain.GetName(), "error", err)
				}
			}
		}
	}
}

func (w *Worker) processBlocks() error {
	latestBlock, err := w.chain.GetLatestBlockNumber()
	if err != nil {
		return fmt.Errorf("failed to get latest block: %w", err)
	}
	slog.Debug("Latest block", "chain", w.chain.GetName(), "block", latestBlock)

	if w.currentBlock >= latestBlock {
		return nil // Nothing to process
	}

	// Process blocks in batches
	endBlock := w.currentBlock + int64(w.config.BatchSize) - 1
	if endBlock > latestBlock {
		endBlock = latestBlock
	}

	blocks, err := w.chain.GetBlocks(w.currentBlock, endBlock)
	if err != nil {
		return fmt.Errorf("failed to get blocks %d-%d: %w", w.currentBlock, endBlock, err)
	}

	for _, block := range blocks {
		// Emit block event
		if err := w.emitter.EmitBlock(w.chain.GetName(), block); err != nil {
			slog.Error("Failed to emit block event", "chain", w.chain.GetName(), "error", err)
		}

		// Emit transaction events
		for _, tx := range block.Transactions {
			slog.Debug("Emitting transaction event", "chain", w.chain.GetName(), "transaction", tx.Hash)
			if err := w.emitter.EmitTransaction(w.chain.GetName(), &tx); err != nil {
				slog.Error("Failed to emit transaction event", "chain", w.chain.GetName(), "error", err)
			}
		}

		slog.Info("Processed block", "chain", w.chain.GetName(), "block", block.Number, "transactions", len(block.Transactions))
	}

	w.currentBlock = endBlock + 1
	return nil
}
