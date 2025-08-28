package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	ModeRegular   WorkerMode = "regular"
	ModeCatchup   WorkerMode = "catchup"
	ModeRescanner WorkerMode = "rescanner"
)

type Worker interface {
	Start()
	Stop()
}

type BaseWorker struct {
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
	logger             *slog.Logger
}

func (bw *BaseWorker) Stop() {
	if bw.currentBlock > 0 {
		_ = bw.blockStore.SaveLatestBlock(bw.chain.GetName(), bw.currentBlock-1)
	}
	bw.cancel()
	if bw.logFile != nil {
		_ = bw.logFile.Close()
		bw.logFile = nil
	}
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
}

func (bw *BaseWorker) run(job func() error) {
	ticker := time.NewTicker(bw.config.PollInterval)
	defer ticker.Stop()

	errorCount := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-bw.ctx.Done():
			if bw.currentBlock > 0 {
				err := bw.blockStore.SaveLatestBlock(bw.chain.GetName(), bw.currentBlock-1)
				if err != nil {
					logger.Error("Error saving latest block", "chain", bw.chain.GetName(), "error", err)
				}
			}
			logger.Info("Worker stopped", "chain", bw.chain.GetName())
			return
		case <-ticker.C:
			if err := job(); err != nil {
				errorCount++
				logger.Error("Error processing blocks", "chain", bw.chain.GetName(), "error", err, "consecutive_errors", errorCount)
				_ = bw.emitter.EmitError(bw.chain.GetName(), err)
				if errorCount >= maxConsecutiveErrors {
					logger.Warn("Too many consecutive errors, slowing down", "chain", bw.chain.GetName())
					time.Sleep(bw.config.PollInterval)
					errorCount = 0
				}
			} else {
				errorCount = 0
			}
		}
	}
}

func (bw *BaseWorker) emitBlock(block *types.Block) {
	if bw.addressBloomFilter == nil {
		return
	}
	addressType := bw.chain.GetAddressType()

	for _, tx := range block.Transactions {
		matched := false
		if bw.addressBloomFilter.Contains(tx.ToAddress, addressType) ||
			bw.addressBloomFilter.Contains(tx.FromAddress, addressType) {
			matched = true
		}

		if matched {
			logger.Info("Emitting transaction",
				"chain", bw.chain.GetName(),
				"from", tx.FromAddress,
				"to", tx.ToAddress,
				"addressType", addressType,
			)
			_ = bw.emitter.EmitTransaction(bw.chain.GetName(), &tx)
		}
	}
}

func (bw *BaseWorker) handleBlockResult(result BlockResult) bool {
	if result.Error != nil {
		err := bw.blockStore.SaveFailedBlock(bw.chain.GetName(), result.Number)
		if err != nil {
			logger.Error("Error saving failed block", "chain", bw.chain.GetName(), "block", result.Number, "error", err)
		}
		logger.Error("Failed block", "chain", bw.chain.GetName(), "block", result.Number, "error", result.Error.Message)
		return false
	}
	if result.Block == nil {
		logger.Error("Nil block", "chain", bw.chain.GetName(), "block", result.Number)
		return false
	}
	bw.emitBlock(result.Block)

	bw.logger.Info("Handling block", "chain", bw.chain.GetName(), "number", result.Block.Number)
	return true
}

func newWorkerWithMode(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter, mode WorkerMode) *BaseWorker {
	ctx, cancel := context.WithCancel(ctx)
	logFile, date, _ := createLogFile()
	logger := logger.With(slog.String("mode", strings.ToUpper(string("["+string(mode)+"]"))))

	return &BaseWorker{
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
		logger:             logger,
	}
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
