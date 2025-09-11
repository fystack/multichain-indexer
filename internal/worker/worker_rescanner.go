package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
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
	failedBlocks map[uint64]int
	maxRetries   int
	interval     time.Duration
}

func NewRescannerWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore *blockstore.Store,
	emitter *events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *RescannerWorker {
	return &RescannerWorker{
		BaseWorker:   newWorkerWithMode(ctx, chain, cfg, kv, blockStore, emitter, pubkeyStore, ModeRescanner, failedChan),
		failedBlocks: make(map[uint64]int),
		maxRetries:   RescannerMaxRetries,
		interval:     RescannerInterval,
	}
}

func (rw *RescannerWorker) Start() {
	rw.logger.Info("Starting rescanner worker",
		"chain", rw.chain.GetName(),
		"interval", rw.interval,
		"maxRetries", rw.maxRetries,
	)

	// Goroutine 1: listen failedChan
	go func() {
		for evt := range rw.failedChan {
			rw.addFailedBlock(evt.Block, fmt.Sprintf("from failedChan attempt %d", evt.Attempt))
		}
	}()

	// Goroutine 2: periodic rescan
	go rw.run(rw.processRescan)
}

func failedBlocksKey(chain string, block uint64) string {
	return fmt.Sprintf("%s/%s/%d", chain, constant.KVPrefixFailedBlocks, block)
}
func failedBlocksPrefix(chain string) string {
	return fmt.Sprintf("%s/%s/", chain, constant.KVPrefixFailedBlocks)
}

// persist helper
func (rw *RescannerWorker) persistBlockInfo(block uint64, retries int, errMsg string) {
	info := FailedBlockInfo{Retries: retries, LastError: errMsg}
	if data, err := json.Marshal(info); err == nil {
		_ = rw.kvstore.Set(failedBlocksKey(rw.chain.GetName(), block), string(data))
	}
}

// add new failed block
func (rw *RescannerWorker) addFailedBlock(block uint64, errMsg string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if _, exists := rw.failedBlocks[block]; !exists {
		rw.failedBlocks[block] = 0
	}
	rw.persistBlockInfo(block, rw.failedBlocks[block], errMsg)
	rw.logger.Info("Added failed block", "block", block, "error", errMsg)
}

// syncFromKV
func (rw *RescannerWorker) syncFromKV() error {
	pairs, err := rw.kvstore.List(failedBlocksPrefix(rw.chain.GetName()))
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
		if block, err := strconv.ParseUint(parts[len(parts)-1], 10, 64); err == nil {
			var info FailedBlockInfo
			_ = json.Unmarshal([]byte(p.Value), &info)
			if info.Retries > rw.failedBlocks[block] {
				rw.failedBlocks[block] = info.Retries
			}
		}
	}
	return nil
}

// remove
func (rw *RescannerWorker) removeBlock(block uint64) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	delete(rw.failedBlocks, block)
	_ = rw.kvstore.Delete(failedBlocksKey(rw.chain.GetName(), block))
}

// retry logic
func (rw *RescannerWorker) incrementRetry(block uint64, errMsg string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if count, ok := rw.failedBlocks[block]; ok {
		if count+1 >= rw.maxRetries {
			rw.logger.Error("Max retries reached; giving up",
				"chain", rw.chain.GetName(), "block", block)
			delete(rw.failedBlocks, block)
			_ = rw.kvstore.Delete(failedBlocksKey(rw.chain.GetName(), block))
		} else {
			rw.failedBlocks[block] = count + 1
			rw.persistBlockInfo(block, rw.failedBlocks[block], errMsg)
		}
	}
}

// main rescan loop
func (rw *RescannerWorker) processRescan() error {
	if err := rw.syncFromKV(); err != nil {
		rw.logger.Error("Failed sync KV", "error", err)
	}

	blocks := rw.collectBlocksForRetry()
	if len(blocks) == 0 {
		time.Sleep(rw.interval)
		return nil
	}
	rw.logger.Info("Got blocks for rescan", "chain", rw.chain.GetName(), "blocks", len(blocks))
	return rw.processBatch(blocks)
}

func (rw *RescannerWorker) collectBlocksForRetry() []uint64 {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	blocks := make([]uint64, 0, len(rw.failedBlocks))
	for b := range rw.failedBlocks {
		blocks = append(blocks, b)
	}
	return blocks
}

func (rw *RescannerWorker) processBatch(blocks []uint64) error {
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
			rw.removeBlock(res.Number)
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
