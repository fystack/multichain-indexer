package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fystack/transaction-indexer/internal/chains"
	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/internal/types"
)

type FailedBlock struct {
	Timestamp string `json:"timestamp"`
	Chain     string `json:"chain"`
	Block     int64  `json:"block"`
	Error     string `json:"error"`
}

type Worker struct {
	config       config.ChainConfig
	chain        chains.ChainIndexer
	emitter      *events.Emitter
	currentBlock int64
	ctx          context.Context
	cancel       context.CancelFunc
	logFile      *os.File
	logFileDate  string
}

func NewWorker(chain chains.ChainIndexer, config config.ChainConfig, emitter *events.Emitter) *Worker {
	ctx, cancel := context.WithCancel(context.Background())

	logFile, date, err := createLogFile()
	if err != nil {
		slog.Error("Failed to create failed block logger", "error", err)
	}

	// Determine starting block
	startBlock := config.StartBlock
	if config.IsLatest {
		// Get the latest block number when IsLatest is true
		latestBlock, err := chain.GetLatestBlockNumber(ctx)
		if err != nil {
			slog.Error("Failed to get latest block number, using configured start block",
				"chain", chain.GetName(), "error", err, "fallback_block", startBlock)
		} else {
			startBlock = latestBlock
			slog.Info("Starting from latest block", "chain", chain.GetName(), "latest_block", startBlock)
		}
	}

	return &Worker{
		chain:        chain,
		config:       config,
		emitter:      emitter,
		currentBlock: startBlock,
		ctx:          ctx,
		cancel:       cancel,
		logFile:      logFile,
		logFileDate:  date,
	}
}

func (w *Worker) Start() {
	go w.run()
}

func (w *Worker) Stop() {
	w.cancel()
	if w.logFile != nil {
		_ = w.logFile.Close()
	}
}

func (w *Worker) run() {
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	slog.Info("Starting indexer", "chain", w.chain.GetName(), "start_block", w.currentBlock)

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			slog.Info("Processing blocks", "chain", w.chain.GetName(), "start_block", w.currentBlock)
			if err := w.processBlocks(); err != nil {
				slog.Error("Error processing blocks", "chain", w.chain.GetName(), "error", err)
				_ = w.emitter.EmitError(w.chain.GetName(), err)
			}
		}
	}
}

func (w *Worker) processBlocks() error {
	latest, err := w.chain.GetLatestBlockNumber(w.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	if w.currentBlock > latest {
		slog.Info("Current block is greater than latest block, waiting for new block", "chain", w.chain.GetName(), "current_block", w.currentBlock, "latest_block", latest)
		time.Sleep(3 * time.Second)
		return nil
	}

	end := min(w.currentBlock+int64(w.config.BatchSize)-1, latest)
	lastSuccess := w.currentBlock - 1

	results, err := w.chain.GetBlocks(w.ctx, w.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get batch blocks: %w", err)
	}

	for _, result := range results {
		if result.Error != nil {
			w.logFailedBlock(result.Number, errors.New(result.Error.Message))
			continue
		}

		if result.Block == nil {
			slog.Error("Block is nil",
				"chain", w.chain.GetName(),
				"block", result.Number,
			)
			w.logFailedBlock(result.Number, fmt.Errorf("block is nil"))
			continue
		}

		w.emitBlock(result.Block)
		lastSuccess = result.Number
	}

	if lastSuccess >= w.currentBlock {
		w.currentBlock = lastSuccess + 1
	}

	return nil
}

func (w *Worker) logFailedBlock(blockNumber int64, err error) {
	// Rotate log file daily if needed
	currentDate := time.Now().Format(time.DateOnly)
	if currentDate != w.logFileDate {
		_ = w.logFile.Close()
		w.logFile, w.logFileDate, _ = createLogFile()
	}

	msg := FailedBlock{
		Timestamp: time.Now().Format(time.RFC3339),
		Chain:     w.chain.GetName(),
		Block:     blockNumber,
		Error:     err.Error(),
	}
	if data, err := json.Marshal(msg); err == nil {
		_, _ = w.logFile.WriteString(string(data) + "\n")
	}
	slog.Error("Failed block", "chain", w.chain.GetName(), "block", blockNumber, "error", err)
	// _ = w.emitter.EmitError(w.chain.GetName(), fmt.Errorf("failed block %d: %w", blockNumber, err))
}

func (w *Worker) emitBlock(block *types.Block) {
	// if err := w.emitter.EmitBlock(w.chain.GetName(), block); err != nil {
	// 	slog.Error("Emit block failed", "chain", w.chain.GetName(), "err", err)
	// }

	for _, tx := range block.Transactions {
		if err := w.emitter.EmitTransaction(w.chain.GetName(), &tx); err != nil {
			slog.Error("Emit transaction failed", "chain", w.chain.GetName(), "tx", tx.TxHash, "err", err)
		}
	}

	slog.Info("Processed block", "chain", w.chain.GetName(), "block", block.Number, "txs", len(block.Transactions))
}

// createLogFile opens the daily failed block log file.
func createLogFile() (*os.File, string, error) {
	date := time.Now().Format(time.DateOnly)
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(logDir, fmt.Sprintf("failed_blocks_%s.log", date))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	return f, date, err
}
