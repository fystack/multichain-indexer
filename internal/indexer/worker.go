package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fystack/indexer/internal/chains"
	"github.com/fystack/indexer/internal/config"
	"github.com/fystack/indexer/internal/events"
)

type Worker struct {
	chain        chains.ChainIndexer
	config       config.ChainConfig
	emitter      *events.Emitter
	currentBlock int64
	ctx          context.Context
	cancel       context.CancelFunc
}

func NewWorker(chain chains.ChainIndexer, config config.ChainConfig, emitter *events.Emitter) *Worker {
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
	slog.Info("Latest block", "chain", w.chain.GetName(), "block", latestBlock)

	if w.currentBlock >= latestBlock {
		return nil // Nothing to process
	}

	endBlock := w.currentBlock + int64(w.config.BatchSize) - 1
	endBlock = min(endBlock, latestBlock)

	var lastSuccessBlock int64 = w.currentBlock - 1

	// Prepare log file
	logFile, err := failedBlockLogger()
	if err != nil {
		return fmt.Errorf("failed to prepare log file: %w", err)
	}
	defer logFile.Close()

	for blockNumber := w.currentBlock; blockNumber <= endBlock; blockNumber++ {
		block, err := w.chain.GetBlock(blockNumber)
		if err != nil {
			msg := fmt.Sprintf("Failed to fetch block: chain=%s block=%d err=%v\n", w.chain.GetName(), blockNumber, err)
			slog.Error(msg)

			// write to log file
			_, _ = logFile.WriteString(msg)

			// emit error for observability
			_ = w.emitter.EmitError(w.chain.GetName(), fmt.Errorf("failed block %d: %w", blockNumber, err))
			continue
		}

		if err := w.emitter.EmitBlock(w.chain.GetName(), block); err != nil {
			slog.Error("Failed to emit block event", "chain", w.chain.GetName(), "error", err)
		}

		for _, tx := range block.Transactions {
			if err := w.emitter.EmitTransaction(w.chain.GetName(), &tx); err != nil {
				slog.Error("Failed to emit transaction event", "chain", w.chain.GetName(), "tx", tx.Hash, "error", err)
			}
		}

		slog.Info("Processed block", "chain", w.chain.GetName(), "block", block.Number, "transactions", len(block.Transactions))
		lastSuccessBlock = block.Number
	}

	// Move currentBlock forward only to last successfully processed block
	if lastSuccessBlock >= w.currentBlock {
		w.currentBlock = lastSuccessBlock + 1
	}

	return nil
}

// failedBlockLogger returns a file handle for appending block errors
func failedBlockLogger() (*os.File, error) {
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	today := time.Now().Format("2006-01-02")
	logFile := filepath.Join(logDir, fmt.Sprintf("failed_blocks_%s.log", today))
	return os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
}
