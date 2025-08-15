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

	"github.com/fystack/transaction-indexer/internal/core"
	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/internal/kvstore"
)

// FailedBlock is a struct for logging failed blocks to file
type FailedBlock struct {
	Timestamp string `json:"timestamp"`
	Chain     string `json:"chain"`
	Block     uint64 `json:"block"`
	Error     string `json:"error"`
}

type Worker struct {
	config           core.ChainConfig
	chain            Indexer
	kvstore          kvstore.KVStore
	failedBlockStore *kvstore.FailedBlockStore
	emitter          *events.Emitter
	currentBlock     uint64
	ctx              context.Context
	cancel           context.CancelFunc
	logFile          *os.File
	logFileDate      string
}

func NewWorker(chain Indexer, config core.ChainConfig, kv kvstore.KVStore, emitter *events.Emitter) *Worker {
	w := newWorkerBase(chain, config, kv, emitter)

	// Determine starting block for regular indexing
	w.currentBlock = w.determineStartingBlock()

	return w
}

func NewFailedBlockWorker(chain Indexer, config core.ChainConfig, kv kvstore.KVStore, emitter *events.Emitter) *Worker {
	// Failed block workers don't need a starting block since they process from the failed blocks store
	return newWorkerBase(chain, config, kv, emitter)
}

func (w *Worker) Start() {
	go w.run(w.processBlocks)
}

func (w *Worker) StartFailedBlocks() {
	go w.run(w.processFailedBlocks)
}

// StartFailedBlocksOneShot processes failed blocks once and then stops
func (w *Worker) StartFailedBlocksOneShot() {
	go w.runOneShot(w.processFailedBlocksOneShot)
}

func (w *Worker) Stop() {
	if w.currentBlock > 0 {
		w.saveLatestBlockNumber(w.currentBlock - 1)
	}
	w.cancel()
	if w.logFile != nil {
		_ = w.logFile.Close()
		w.logFile = nil
	}
}

func (w *Worker) run(job func() error) {
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	slog.Info("Starting worker", "chain", w.chain.GetName(), "start_block", w.currentBlock)

	errorCount := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-w.ctx.Done():
			if w.currentBlock > 0 {
				w.saveLatestBlockNumber(w.currentBlock - 1)
			}
			slog.Info("Worker stopped", "chain", w.chain.GetName())
			return
		case <-ticker.C:
			if err := job(); err != nil {
				errorCount++
				slog.Error("Error processing blocks", "chain", w.chain.GetName(), "error", err, "consecutive_errors", errorCount)

				// Emit error but don't fail completely
				_ = w.emitter.EmitError(w.chain.GetName(), err)

				// If too many consecutive errors, increase poll interval temporarily
				if errorCount >= maxConsecutiveErrors {
					slog.Warn("Too many consecutive errors, slowing down", "chain", w.chain.GetName())
					time.Sleep(w.config.PollInterval)
					errorCount = 0 // Reset counter after backing off
				}
			} else {
				// Reset error count on success
				errorCount = 0
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
		slog.Debug("Current block is greater than latest block, waiting", "chain", w.chain.GetName(), "current", w.currentBlock, "latest", latest)
		time.Sleep(3 * time.Second)
		return nil
	}

	end := min(w.currentBlock+uint64(w.config.BatchSize)-1, latest)
	lastSuccess := w.currentBlock - 1
	slog.Info("Processing blocks", "chain", w.chain.GetName(), "start", w.currentBlock, "end", end)

	results, err := w.chain.GetBlocks(w.ctx, w.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get batch blocks: %w", err)
	}

	for _, result := range results {
		if w.handleBlockResult(result, false) {
			lastSuccess = result.Number
		}
	}

	if lastSuccess >= w.currentBlock {
		w.currentBlock = lastSuccess + 1
		w.saveLatestBlockNumber(lastSuccess)
	}

	return nil
}

func (w *Worker) processFailedBlocks() error {
	failedBlocks, err := w.failedBlockStore.GetFailedBlocks(w.chain.GetName())
	if err != nil {
		return fmt.Errorf("get failed blocks: %w", err)
	}

	if len(failedBlocks) == 0 {
		slog.Info("No failed blocks to process, stopping worker", "chain", w.chain.GetName())
		// Signal to stop the worker by canceling the context
		w.cancel()
		return nil
	}

	slog.Info("Processing failed blocks", "chain", w.chain.GetName(), "count", len(failedBlocks))

	results, err := w.chain.GetBlocksByNumbers(w.ctx, failedBlocks)
	if err != nil {
		return fmt.Errorf("get failed blocks: %w", err)
	}

	successCount := 0
	for _, result := range results {
		if w.handleBlockResult(result, true) {
			successCount++
		}
	}

	if successCount > 0 {
		slog.Info("Resolved failed blocks", "chain", w.chain.GetName(), "resolved", successCount, "total", len(results))
	}

	// Check if there are still failed blocks remaining after processing
	remainingBlocks, err := w.failedBlockStore.GetFailedBlocks(w.chain.GetName())
	if err != nil {
		slog.Error("Failed to check remaining failed blocks", "chain", w.chain.GetName(), "error", err)
		return nil
	}

	if len(remainingBlocks) == 0 {
		slog.Info("All failed blocks processed, stopping worker", "chain", w.chain.GetName())
		w.cancel()
	}

	return nil
}

// runOneShot runs a job once and then stops
func (w *Worker) runOneShot(job func() error) {
	slog.Info("Starting one-shot worker", "chain", w.chain.GetName())

	defer func() {
		slog.Info("One-shot worker completed", "chain", w.chain.GetName())
	}()

	if err := job(); err != nil {
		slog.Error("Error in one-shot processing", "chain", w.chain.GetName(), "error", err)
		_ = w.emitter.EmitError(w.chain.GetName(), err)
	}
}

// processFailedBlocksOneShot processes all failed blocks once and stops
func (w *Worker) processFailedBlocksOneShot() error {
	failedBlocks, err := w.failedBlockStore.GetFailedBlocks(w.chain.GetName())
	if err != nil {
		return fmt.Errorf("get failed blocks: %w", err)
	}

	if len(failedBlocks) == 0 {
		slog.Info("No failed blocks to process", "chain", w.chain.GetName())
		return nil
	}

	slog.Info("Processing failed blocks (one-shot)", "chain", w.chain.GetName(), "count", len(failedBlocks))

	results, err := w.chain.GetBlocksByNumbers(w.ctx, failedBlocks)
	if err != nil {
		return fmt.Errorf("get failed blocks: %w", err)
	}

	successCount := 0
	for _, result := range results {
		if w.handleBlockResult(result, true) {
			successCount++
		}
	}

	slog.Info("Completed processing failed blocks", "chain", w.chain.GetName(),
		"resolved", successCount, "total", len(results), "failed", len(results)-successCount)

	return nil
}

func (w *Worker) logFailedBlock(blockNumber uint64, err error) {
	// Store in FailedBlockStore (primary storage)
	if storeErr := w.failedBlockStore.StoreFailedBlock(w.chain.GetName(), blockNumber, err); storeErr != nil {
		slog.Error("Failed to store failed block in kvstore", "chain", w.chain.GetName(), "block", blockNumber, "error", storeErr)
	}

	// File logging for backup/debugging (only if file is available)
	if w.logFile != nil {
		w.rotateLogFileIfNeeded()
		w.writeToLogFile(blockNumber, err)
	}

	slog.Error("Failed block", "chain", w.chain.GetName(), "block", blockNumber, "error", err)
}

// rotateLogFileIfNeeded rotates the log file daily
func (w *Worker) rotateLogFileIfNeeded() {
	currentDate := time.Now().Format(time.DateOnly)
	if currentDate != w.logFileDate {
		_ = w.logFile.Close()
		w.logFile, w.logFileDate, _ = createLogFile()
	}
}

// writeToLogFile writes failed block info to the log file
func (w *Worker) writeToLogFile(blockNumber uint64, err error) {
	msg := FailedBlock{
		Timestamp: time.Now().Format(time.RFC3339),
		Chain:     w.chain.GetName(),
		Block:     blockNumber,
		Error:     err.Error(),
	}
	if data, marshalErr := json.Marshal(msg); marshalErr == nil {
		_, _ = w.logFile.WriteString(string(data) + "\n")
	}
}

func (w *Worker) emitBlock(block *core.Block) {
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

// GetFailedBlocksInfo retrieves detailed information about all failed blocks for the worker's chain
func (w *Worker) GetFailedBlocksInfo() ([]*kvstore.FailedBlockInfo, error) {
	return w.failedBlockStore.GetAllFailedBlocks(w.chain.GetName())
}

// GetFailedBlocksCount returns the count of unresolved failed blocks for the worker's chain
func (w *Worker) GetFailedBlocksCount() (int, error) {
	failedBlocks, err := w.failedBlockStore.GetFailedBlocks(w.chain.GetName())
	if err != nil {
		return 0, err
	}
	return len(failedBlocks), nil
}

// CleanupOldResolvedBlocks removes resolved failed blocks older than the specified duration
func (w *Worker) CleanupOldResolvedBlocks(olderThan time.Duration) error {
	return w.failedBlockStore.CleanupResolvedBlocks(w.chain.GetName(), olderThan)
}

// ResolveFailedBlock manually marks a failed block as resolved
func (w *Worker) ResolveFailedBlock(blockNumber uint64) error {
	return w.failedBlockStore.ResolveFailedBlock(w.chain.GetName(), blockNumber)
}

// newWorkerBase creates a worker with common initialization
func newWorkerBase(chain Indexer, config core.ChainConfig, kv kvstore.KVStore, emitter *events.Emitter) *Worker {
	ctx, cancel := context.WithCancel(context.Background())

	logFile, date, err := createLogFile()
	if err != nil {
		slog.Error("Failed to create failed block logger", "chain", chain.GetName(), "error", err)
	}

	return &Worker{
		chain:            chain,
		config:           config,
		kvstore:          kv,
		failedBlockStore: kvstore.NewFailedBlockStore(kv),
		emitter:          emitter,
		ctx:              ctx,
		cancel:           cancel,
		logFile:          logFile,
		logFileDate:      date,
	}
}

// determineStartingBlock determines the starting block for regular indexing
func (w *Worker) determineStartingBlock() uint64 {
	// First try to get from kvstore
	latestBlock, err := w.getLatestBlockNumber()
	if err != nil {
		slog.Debug("Failed to get latest block from kvstore", "chain", w.chain.GetName(), "error", err)
	}

	if latestBlock == 0 {
		latestBlock = uint64(w.config.StartBlock)
	}

	// If FromLatest flag is set, get the latest block from chain
	if w.config.FromLatest {
		chainLatest, err := w.chain.GetLatestBlockNumber(w.ctx)
		if err != nil {
			slog.Error("Failed to get latest block from chain, using fallback",
				"chain", w.chain.GetName(), "error", err, "fallback", latestBlock)
		} else {
			latestBlock = chainLatest
		}
	}

	return latestBlock
}

// handleBlockResult processes a block result and returns true if successful
func (w *Worker) handleBlockResult(result BlockResult, isFailedBlock bool) bool {
	if result.Error != nil {
		blockErr := errors.New(result.Error.Message)
		if isFailedBlock {
			// For failed block processing, store with updated retry count
			if err := w.failedBlockStore.StoreFailedBlock(w.chain.GetName(), result.Number, blockErr); err != nil {
				slog.Error("Failed to store failed block", "chain", w.chain.GetName(), "block", result.Number, "error", err)
			}
		}
		w.logFailedBlock(result.Number, blockErr)
		return false
	}

	if result.Block == nil {
		blockErr := fmt.Errorf("block is nil")
		if isFailedBlock {
			// For failed block processing, store with updated retry count
			if err := w.failedBlockStore.StoreFailedBlock(w.chain.GetName(), result.Number, blockErr); err != nil {
				slog.Error("Failed to store failed block", "chain", w.chain.GetName(), "block", result.Number, "error", err)
			}
		}
		w.logFailedBlock(result.Number, blockErr)
		return false
	}

	// Successfully processed block
	if isFailedBlock {
		// Mark as resolved for failed block processing
		if err := w.failedBlockStore.ResolveFailedBlock(w.chain.GetName(), result.Number); err != nil {
			slog.Error("Failed to resolve failed block", "chain", w.chain.GetName(), "block", result.Number, "error", err)
		}
	}

	w.emitBlock(result.Block)
	return true
}
