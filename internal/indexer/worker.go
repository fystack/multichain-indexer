package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"idx/internal/core"
	"idx/internal/events"
	"idx/internal/kvstore"
)

type FailedBlock struct {
	Timestamp string `json:"timestamp"`
	Chain     string `json:"chain"`
	Block     uint64 `json:"block"`
	Error     string `json:"error"`
}

type Worker struct {
	config       core.ChainConfig
	chain        Indexer
	kvstore      kvstore.KVStore
	emitter      *events.Emitter
	currentBlock uint64
	ctx          context.Context
	cancel       context.CancelFunc
	logFile      *os.File
	logFileDate  string
}

func NewWorker(chain Indexer, config core.ChainConfig, kvstore kvstore.KVStore, emitter *events.Emitter) *Worker {
	ctx, cancel := context.WithCancel(context.Background())

	logFile, date, err := createLogFile()
	if err != nil {
		slog.Error("Failed to create failed block logger", "error", err)
	}

	w := &Worker{
		chain:       chain,
		config:      config,
		kvstore:     kvstore,
		emitter:     emitter,
		ctx:         ctx,
		cancel:      cancel,
		logFile:     logFile,
		logFileDate: date,
	}

	// Determine starting block
	// First try to get from kvstore
	// If not found, then get from config
	// If latest flag is true, then get the latest block number from chain
	latestBlock, err := w.getLatestBlockNumber()
	if err != nil {
		slog.Error("Failed to get latest block number from kvstore, using configured start block",
			"chain", chain.GetName(), "error", err, "fallback_block", config.StartBlock)
	}
	if latestBlock == 0 {
		latestBlock = uint64(config.StartBlock)
	}
	if config.FromLatest {
		latestBlock, err = chain.GetLatestBlockNumber(ctx)
		if err != nil {
			slog.Error("Failed to get latest block number, using configured start block",
				"chain", chain.GetName(), "error", err, "fallback_block", config.StartBlock)
		}
	}
	w.currentBlock = latestBlock

	return w
}

func (w *Worker) Start() {
	go w.run()
}

func (w *Worker) Stop() {
	w.saveLatestBlockNumber(w.currentBlock - 1)
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
			w.saveLatestBlockNumber(w.currentBlock - 1)
			return
		case <-ticker.C:
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

	end := min(w.currentBlock+uint64(w.config.BatchSize)-1, latest)
	lastSuccess := w.currentBlock - 1
	slog.Info("Processing blocks", "chain", w.chain.GetName(), "start_block", w.currentBlock, "end_block", end)

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
			w.logFailedBlock(result.Number, fmt.Errorf("block is nil"))
			continue
		}

		w.emitBlock(result.Block)
		lastSuccess = result.Number
	}

	if lastSuccess >= w.currentBlock {
		w.currentBlock = lastSuccess + 1
		w.saveLatestBlockNumber(lastSuccess)
	}

	return nil
}

func (w *Worker) logFailedBlock(blockNumber uint64, err error) {
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

func (w *Worker) emitBlock(block *core.Block) {
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

func (w *Worker) getLatestBlockNumber() (uint64, error) {
	key := fmt.Sprintf("latest_block_%s", w.chain.GetName())
	latestBlock, err := w.kvstore.Get(key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(latestBlock), 10, 64)
}

func (w *Worker) saveLatestBlockNumber(blockNumber uint64) {
	key := fmt.Sprintf("latest_block_%s", w.chain.GetName())
	_ = w.kvstore.Set(key, []byte(strconv.FormatUint(blockNumber, 10)))
	slog.Debug("Saved latest processed block", "chain", w.chain.GetName(), "block", blockNumber)
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
