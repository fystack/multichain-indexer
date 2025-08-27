package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/infra"
)

// WorkerMode defines the mode of operation for a worker
type WorkerMode string

const (
	ModeRegular WorkerMode = "regular"
	ModeCatchup WorkerMode = "catchup"
)

type Worker struct {
	config             config.ChainConfig
	chain              Indexer
	kvstore            infra.KVStore
	blockStore         *BlockStore
	emitter            *events.Emitter
	currentBlock       uint64
	ctx                context.Context
	cancel             context.CancelFunc
	logFile            *os.File
	logFileDate        string
	addressBloomFilter addressbloomfilter.WalletAddressBloomFilter
	mode               WorkerMode
	catchupStart       uint64
	catchupEnd         uint64
}

// NewWorker creates a worker for regular indexing
func NewWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter) *Worker {
	return newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, addressBF, ModeRegular, 0, 0)
}

// NewCatchupWorker creates a worker for historical range
func NewCatchupWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter, startBlock, endBlock uint64) *Worker {
	return newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, addressBF, ModeCatchup, startBlock, endBlock)
}

// Start the worker
func (w *Worker) Start() {
	switch w.mode {
	case ModeRegular:
		logger.Info("Starting regular worker", "chain", w.chain.GetName(), "start_block", w.currentBlock)
		go w.run(w.processBlocks)
	case ModeCatchup:
		logger.Info("Starting catchup worker", "chain", w.chain.GetName(), "start_block", w.currentBlock)
		go w.run(w.processBlocks)
	default:
		logger.Error("Unknown worker mode", "mode", w.mode, "chain", w.chain.GetName())
	}
}

func (w *Worker) Stop() {
	if w.currentBlock > 0 {
		_ = w.blockStore.SaveLatestBlock(w.chain.GetName(), w.currentBlock-1)
	}
	w.cancel()
	if w.logFile != nil {
		_ = w.logFile.Close()
		w.logFile = nil
	}
	if w.kvstore != nil {
		_ = w.kvstore.Close()
		w.kvstore = nil
	}
	if w.emitter != nil {
		w.emitter.Close()
		w.emitter = nil
	}
	if w.blockStore != nil {
		_ = w.blockStore.Close()
		w.blockStore = nil
	}
}

func (w *Worker) run(job func() error) {
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	errorCount := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-w.ctx.Done():
			if w.currentBlock > 0 {
				err := w.blockStore.SaveLatestBlock(w.chain.GetName(), w.currentBlock-1)
				if err != nil {
					logger.Error("Error saving latest block", "chain", w.chain.GetName(), "error", err)
				}
			}
			logger.Info("Worker stopped", "chain", w.chain.GetName())
			return
		case <-ticker.C:
			if err := job(); err != nil {
				errorCount++
				logger.Error("Error processing blocks", "chain", w.chain.GetName(), "error", err, "consecutive_errors", errorCount)
				_ = w.emitter.EmitError(w.chain.GetName(), err)
				if errorCount >= maxConsecutiveErrors {
					logger.Warn("Too many consecutive errors, slowing down", "chain", w.chain.GetName())
					time.Sleep(w.config.PollInterval)
					errorCount = 0
				}
			} else {
				errorCount = 0
			}
		}
	}
}

func (w *Worker) processBlocks() error {
	switch w.mode {
	case ModeRegular:
		return w.processRegularBlocks()
	case ModeCatchup:
		return w.processCatchupBlocks()
	default:
		return fmt.Errorf("unknown worker mode: %s", w.mode)
	}
}

func (w *Worker) processRegularBlocks() error {
	latest, err := w.chain.GetLatestBlockNumber(w.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	if w.currentBlock > latest {
		time.Sleep(3 * time.Second)
		return nil
	}

	end := min(w.currentBlock+uint64(w.config.BatchSize)-1, latest)
	lastSuccess := w.currentBlock - 1

	start := time.Now()
	results, err := w.chain.GetBlocks(w.ctx, w.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get batch blocks: %w", err)
	}
	elapsed := time.Since(start)
	logger.Info(
		"Processing latest blocks",
		"chain",
		w.chain.GetName(),
		"start",
		w.currentBlock,
		"end",
		end,
		"elapsed",
		elapsed,
		"last_success",
		lastSuccess,
		"expected number of blocks",
		end-w.currentBlock+1,
		"actual blocks",
		len(results),
	)

	for _, result := range results {
		if w.handleBlockResult(result) {
			lastSuccess = result.Number
		}
	}

	if lastSuccess >= w.currentBlock {
		w.currentBlock = lastSuccess + 1
		_ = w.blockStore.SaveLatestBlock(w.chain.GetName(), lastSuccess)
	}
	return nil
}

func (w *Worker) processCatchupBlocks() error {
	if w.currentBlock > w.catchupEnd {
		logger.Info("Catchup completed", "chain", w.chain.GetName(),
			"start", w.catchupStart, "end", w.catchupEnd)
		w.cancel()
		return nil
	}

	end := min(w.currentBlock+uint64(w.config.BatchSize)-1, w.catchupEnd)
	lastSuccess := w.currentBlock - 1

	start := time.Now()
	results, err := w.chain.GetBlocks(w.ctx, w.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get catchup blocks: %w", err)
	}

	for _, result := range results {
		if w.handleBlockResult(result) {
			if result.Number > lastSuccess {
				lastSuccess = result.Number
			}
		}
	}

	elapsed := time.Since(start)
	logger.Info(
		"Processing catchup blocks",
		"chain",
		w.chain.GetName(),
		"start",
		w.currentBlock,
		"end",
		end,
		"elapsed",
		elapsed,
		"last_success",
		lastSuccess,
		"expected number of blocks",
		end-w.currentBlock+1,
		"actual blocks",
		len(results),
	)

	// Update current block to the next block to process
	// Use the last successfully processed block + 1, but don't exceed catchupEnd + 1
	if lastSuccess >= w.currentBlock {
		w.currentBlock = lastSuccess + 1
	} else {
		w.currentBlock = end + 1
	}

	// Ensure we don't exceed the catchup end boundary
	if w.currentBlock > w.catchupEnd+1 {
		w.currentBlock = w.catchupEnd + 1
	}

	w.saveCatchupProgress()
	return nil
}

func (w *Worker) emitBlock(block *types.Block) {
	if w.addressBloomFilter == nil {
		return
	}
	addressType := w.chain.GetAddressType()

	for _, tx := range block.Transactions {
		matched := false
		if w.addressBloomFilter.Contains(tx.ToAddress, addressType) ||
			w.addressBloomFilter.Contains(tx.FromAddress, addressType) {
			matched = true
		}

		if matched {
			logger.Info("Emitting transaction",
				"chain", w.chain.GetName(),
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"addressType", addressType,
			)
			_ = w.emitter.EmitTransaction(w.chain.GetName(), &tx)
		}
	}
}

func (w *Worker) saveCatchupProgress() {
	if w.mode != ModeCatchup {
		return
	}
	key := fmt.Sprintf("catchup_progress_%s_%d_%d", w.chain.GetName(), w.catchupStart, w.catchupEnd)
	progress := fmt.Sprintf("%d", w.currentBlock)
	_ = w.kvstore.Set(key, progress)
}

func (w *Worker) loadCatchupProgress() uint64 {
	if w.mode != ModeCatchup {
		return w.catchupStart
	}
	key := fmt.Sprintf("catchup_progress_%s_%d_%d", w.chain.GetName(), w.catchupStart, w.catchupEnd)
	if data, err := w.kvstore.Get(key); err == nil {
		if progress, err := strconv.ParseUint(string(data), 10, 64); err == nil {
			return progress
		}
	}
	return w.catchupStart
}

func newWorkerWithMode(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter, mode WorkerMode, startBlock, endBlock uint64) *Worker {
	ctx, cancel := context.WithCancel(ctx)
	logFile, date, _ := createLogFile()

	w := &Worker{
		chain:              chain,
		config:             config,
		kvstore:            kv,
		blockStore:         blockStore,
		emitter:            emitter,
		addressBloomFilter: addressBF,
		ctx:                ctx,
		cancel:             cancel,
		logFile:            logFile,
		logFileDate:        date,
		mode:               mode,
		catchupStart:       startBlock,
		catchupEnd:         endBlock,
	}

	switch mode {
	case ModeRegular:
		w.currentBlock = w.determineStartingBlock()
	case ModeCatchup:
		w.currentBlock = w.loadCatchupProgress()
	}
	return w
}

func (w *Worker) determineStartingBlock() uint64 {
	latestBlock, err := w.blockStore.GetLatestBlock(w.chain.GetName())
	if err != nil || latestBlock == 0 {
		latestBlock = uint64(w.config.StartBlock)
	}
	if w.config.FromLatest {
		chainLatest, err := w.chain.GetLatestBlockNumber(w.ctx)
		if err == nil {
			latestBlock = chainLatest
		}
	}
	return latestBlock
}

func (w *Worker) handleBlockResult(result BlockResult) bool {
	if result.Error != nil {
		logger.Error("Failed block", "chain", w.chain.GetName(), "block", result.Number, "error", result.Error.Message)
		return false
	}
	if result.Block == nil {
		logger.Error("Nil block", "chain", w.chain.GetName(), "block", result.Number)
		return false
	}
	w.emitBlock(result.Block)
	// Log block handling with worker mode and elapsed time
	var modePrefix string
	switch w.mode {
	case ModeCatchup:
		modePrefix = "[CATCHUP WORKER]"
	case ModeRegular:
		modePrefix = "[REGULAR WORKER]"
	}

	logger.Info("Handling block", "mode", modePrefix, "chain", w.chain.GetName(), "number", result.Block.Number)
	return true
}

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
