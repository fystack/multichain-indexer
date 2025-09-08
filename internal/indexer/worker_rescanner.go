package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/pubkeystore"
)

const (
	RescannerMaxRetries = 3
	RescannerInterval   = 10 * time.Second
)

// KV schema
type FailedBlockInfo struct {
	Retries   int    `json:"retries"`
	LastError string `json:"last_error,omitempty"`
}

type RescannerWorker struct {
	*BaseWorker
	mu           sync.Mutex
	failedBlocks map[uint64]int // blockNumber -> retryCount (cache local)
	maxRetries   int
	interval     time.Duration
}

func NewRescannerWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, pubkeyStore pubkeystore.Store, failedChan chan FailedBlockEvent) *RescannerWorker {
	worker := newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, pubkeyStore, ModeRescanner, failedChan)
	return &RescannerWorker{
		BaseWorker:   worker,
		failedBlocks: make(map[uint64]int),
		maxRetries:   RescannerMaxRetries,
		interval:     RescannerInterval,
	}
}

// Start launches goroutines for channel + periodic rescan
func (rw *RescannerWorker) Start() {
	rw.logger.Info("Starting rescanner worker",
		"chain", rw.chain.GetName(),
		"interval", rw.interval,
		"maxRetries", rw.maxRetries,
	)

	// Goroutine 1: sync failedChan from BaseWorker
	go func() {
		for evt := range rw.failedChan {
			rw.AddFailedBlock(evt.Block, fmt.Sprintf("from failedChan attempt %d", evt.Attempt))
		}
	}()

	// Goroutine 2: periodic scan cache + KVStore
	go rw.run(rw.processRescan)
}

// key helpers for KVStore
func failedKey(chain string, block uint64) string {
	return fmt.Sprintf("failed_blocks/%s/%d", chain, block)
}
func failedPrefix(chain string) string {
	return fmt.Sprintf("failed_blocks/%s/", chain)
}

// AddFailedBlock add block to cache + KVStore
func (rw *RescannerWorker) AddFailedBlock(block uint64, errMsg string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if _, exists := rw.failedBlocks[block]; !exists {
		rw.failedBlocks[block] = 0
	}

	info := FailedBlockInfo{Retries: rw.failedBlocks[block], LastError: errMsg}
	data, _ := json.Marshal(info)
	_ = rw.kvstore.Set(failedKey(rw.chain.GetName(), block), string(data))
	rw.logger.Info("Added failed block", "block", block, "error", errMsg)
}

// processRescan process retry blocks in cache
func (rw *RescannerWorker) processRescan() error {
	// 1. Sync KVStore → cache
	if err := rw.syncFromKV(); err != nil {
		rw.logger.Error("Failed to sync failed blocks from KVStore", "error", err)
	}

	// 2. Copy cache to process
	rw.mu.Lock()
	blocks := make([]uint64, 0, len(rw.failedBlocks))
	for b := range rw.failedBlocks {
		blocks = append(blocks, b)
	}
	rw.mu.Unlock()

	if len(blocks) == 0 {
		time.Sleep(rw.interval)
		return nil
	}

	// 3. Process by batch
	batchSize := 20
	if len(blocks) < batchSize {
		batchSize = len(blocks)
	}
	batch := blocks[:batchSize]

	results, err := rw.chain.GetBlocksByNumbers(rw.ctx, batch)
	if err != nil {
		return fmt.Errorf("rescanner get blocks: %w", err)
	}

	success := 0
	for _, res := range results {
		if rw.handleBlockResult(res) {
			success++
			rw.removeBlock(res.Number) // remove cache + KVStore cache
		} else {
			rw.incrementRetry(res.Number, "handleBlock failed")
		}
	}

	rw.logger.Info("Rescanner pass",
		"chain", rw.chain.GetName(),
		"retried", len(batch),
		"success", success,
		"remaining", len(rw.failedBlocks),
	)
	return nil
}

// syncFromKV sync KVStore → cache
func (rw *RescannerWorker) syncFromKV() error {
	pairs, err := rw.kvstore.List(failedPrefix(rw.chain.GetName()))
	if err != nil {
		return err
	}

	rw.mu.Lock()
	defer rw.mu.Unlock()

	for _, p := range pairs {
		parts := strings.Split(p.Key, "/")
		if len(parts) < 3 {
			continue
		}
		blockStr := parts[len(parts)-1]
		blockNum, err := strconv.ParseUint(blockStr, 10, 64)
		if err != nil {
			continue
		}

		var info FailedBlockInfo
		_ = json.Unmarshal([]byte(p.Value), &info)

		if _, exists := rw.failedBlocks[blockNum]; !exists || info.Retries > rw.failedBlocks[blockNum] {
			rw.failedBlocks[blockNum] = info.Retries
		}
	}
	return nil
}

// removeBlock remove block from cache + KVStore
func (rw *RescannerWorker) removeBlock(block uint64) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	delete(rw.failedBlocks, block)
	_ = rw.kvstore.Delete(failedKey(rw.chain.GetName(), block))
}

// incrementRetry increase retry count, remove block if over maxRetries
func (rw *RescannerWorker) incrementRetry(block uint64, errMsg string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if count, ok := rw.failedBlocks[block]; ok {
		if count+1 >= rw.maxRetries {
			rw.logger.Error("Max retries reached, giving up on block",
				"chain", rw.chain.GetName(),
				"block", block)
			delete(rw.failedBlocks, block)
			_ = rw.kvstore.Delete(failedKey(rw.chain.GetName(), block))
		} else {
			rw.failedBlocks[block] = count + 1
			info := FailedBlockInfo{Retries: rw.failedBlocks[block], LastError: errMsg}
			data, _ := json.Marshal(info)
			_ = rw.kvstore.Set(failedKey(rw.chain.GetName(), block), string(data))
		}
	}
}
