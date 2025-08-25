package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
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
	kvstore            kvstore.KVStore
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
func NewWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv kvstore.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter) *Worker {
	return newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, addressBF, ModeRegular, 0, 0)
}

// NewCatchupWorker creates a worker for historical range
func NewCatchupWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv kvstore.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter, startBlock, endBlock uint64) *Worker {
	return newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, addressBF, ModeCatchup, startBlock, endBlock)
}

// Start the worker
func (w *Worker) Start() {
	switch w.mode {
	case ModeRegular:
		go w.run(w.processBlocks)
	case ModeCatchup:
		go w.run(w.processBlocks)
	default:
		slog.Error("Unknown worker mode", "mode", w.mode, "chain", w.chain.GetName())
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

	slog.Info("Starting worker", "chain", w.chain.GetName(), "start_block", w.currentBlock)

	errorCount := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-w.ctx.Done():
			if w.currentBlock > 0 {
				_ = w.blockStore.SaveLatestBlock(w.chain.GetName(), w.currentBlock-1)
			}
			slog.Info("Worker stopped", "chain", w.chain.GetName())
			return
		case <-ticker.C:
			if err := job(); err != nil {
				errorCount++
				slog.Error("Error processing blocks", "chain", w.chain.GetName(), "error", err, "consecutive_errors", errorCount)
				_ = w.emitter.EmitError(w.chain.GetName(), err)
				if errorCount >= maxConsecutiveErrors {
					slog.Warn("Too many consecutive errors, slowing down", "chain", w.chain.GetName())
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

	results, err := w.chain.GetBlocks(w.ctx, w.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get batch blocks: %w", err)
	}

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
		slog.Info("Catchup completed", "chain", w.chain.GetName(),
			"start", w.catchupStart, "end", w.catchupEnd)
		w.cancel()
		return nil
	}

	end := min(w.currentBlock+uint64(w.config.BatchSize)-1, w.catchupEnd)
	lastSuccess := w.currentBlock - 1

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

	w.currentBlock = end + 1
	w.saveCatchupProgress()
	return nil
}

func (w *Worker) emitBlock(block *types.Block) {
	if w.addressBloomFilter == nil {
		return
	}
	addressType, ok := w.getAddressTypeForChain()
	if !ok {
		return
	}

	for _, tx := range block.Transactions {
		matched := false

		if w.addressBloomFilter.Contains(tx.ToAddress, addressType) ||
			w.addressBloomFilter.Contains(tx.FromAddress, addressType) {
			matched = true
		}

		if matched {
			slog.Info("Emitting transaction",
				"chain", w.chain.GetName(),
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"addressType", addressType,
			)
			_ = w.emitter.EmitTransaction(w.chain.GetName(), &tx)
		}
	}
}

// getAddressTypeForChain determines address type by checking substrings
func (w *Worker) getAddressTypeForChain() (enum.AddressType, bool) {
	chainName := strings.ToLower(w.chain.GetName())

	switch {
	case strings.Contains(chainName, "evm"), strings.Contains(chainName, "eth"):
		return enum.AddressTypeEvm, true
	case strings.Contains(chainName, "tron"):
		return enum.AddressTypeTron, true
	case strings.Contains(chainName, "sol"):
		return enum.AddressTypeSolana, true
	case strings.Contains(chainName, "btc"), strings.Contains(chainName, "bitcoin"):
		return enum.AddressTypeBtc, true
	case strings.Contains(chainName, "aptos"):
		return enum.AddressTypeAptos, true
	default:
		return enum.AddressTypeEvm, false // fallback but mark unsupported
	}
}

func (w *Worker) saveCatchupProgress() {
	if w.mode != ModeCatchup {
		return
	}
	key := fmt.Sprintf("catchup_progress_%s_%d_%d", w.chain.GetName(), w.catchupStart, w.catchupEnd)
	progress := fmt.Sprintf("%d", w.currentBlock)
	_ = w.kvstore.Set(key, []byte(progress))
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

func newWorkerWithMode(ctx context.Context, chain Indexer, config config.ChainConfig, kv kvstore.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter, mode WorkerMode, startBlock, endBlock uint64) *Worker {
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
		slog.Error("Failed block", "chain", w.chain.GetName(), "block", result.Number, "error", result.Error.Message)
		return false
	}
	if result.Block == nil {
		slog.Error("Nil block", "chain", w.chain.GetName(), "block", result.Number)
		return false
	}
	w.emitBlock(result.Block)
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
