package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const (
	RescannerMaxRetries = 3
	RescannerInterval   = 10 * time.Second

	BatchFlushSize     = 10
	BatchFlushInterval = 5 * time.Second
)

type RescannerWorker struct {
	*BaseWorker
	mu           sync.Mutex
	failedBlocks map[uint64]uint8
	maxRetries   uint8
	interval     time.Duration

	// batching
	batchMu        sync.Mutex
	pendingSaves   []uint64
	pendingRemoves []uint64
}

func NewRescannerWorker(
	ctx context.Context,
	chain indexer.Indexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *RescannerWorker {
	return &RescannerWorker{
		BaseWorker: newWorkerWithMode(
			ctx,
			chain,
			cfg,
			kv,
			blockStore,
			emitter,
			pubkeyStore,
			ModeRescanner,
			failedChan,
		),
		failedBlocks:   make(map[uint64]uint8),
		maxRetries:     RescannerMaxRetries,
		interval:       RescannerInterval,
		pendingSaves:   make([]uint64, 0, BatchFlushSize),
		pendingRemoves: make([]uint64, 0, BatchFlushSize),
	}
}

func (rw *RescannerWorker) Start() {
	rw.logger.Info("Starting rescanner worker",
		"chain", rw.chain.GetName(),
		"interval", rw.interval,
		"maxRetries", rw.maxRetries,
	)

	// listen failedChan
	go func() {
		for evt := range rw.failedChan {
			rw.addFailedBlock(evt.Block, fmt.Sprintf("from failedChan attempt %d", evt.Attempt))
		}
	}()

	// periodic rescan
	go rw.run(rw.processRescan)

	// periodic flush
	go rw.periodicBatchFlush()
}

func (rw *RescannerWorker) Stop() {
	rw.flush()
	rw.BaseWorker.Stop()
}

func (rw *RescannerWorker) periodicBatchFlush() {
	ticker := time.NewTicker(BatchFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rw.ctx.Done():
			rw.flush()
			return
		case <-ticker.C:
			rw.flush()
		}
	}
}

func (rw *RescannerWorker) addFailedBlock(block uint64, errMsg string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if _, exists := rw.failedBlocks[block]; !exists {
		rw.failedBlocks[block] = 0
		rw.addSave(block)
		rw.logger.Info("Added failed block", "block", block, "error", errMsg)
	}
}

func (rw *RescannerWorker) syncFromKV() error {
	blocks, err := rw.blockStore.GetFailedBlocks(rw.chain.GetNetworkInternalCode())
	if err != nil {
		return err
	}
	rw.mu.Lock()
	defer rw.mu.Unlock()
	for _, num := range blocks {
		if _, ok := rw.failedBlocks[num]; !ok {
			rw.failedBlocks[num] = 0
		}
	}
	return nil
}

func (rw *RescannerWorker) removeBlocks(blocks []uint64) {
	if len(blocks) == 0 {
		return
	}
	rw.mu.Lock()
	for _, b := range blocks {
		delete(rw.failedBlocks, b)
	}
	rw.mu.Unlock()
	rw.addRemove(blocks...)
}

func (rw *RescannerWorker) incrementRetry(block uint64) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if count, ok := rw.failedBlocks[block]; ok {
		if count+1 >= rw.maxRetries {
			delete(rw.failedBlocks, block)
			rw.addRemove(block)
			rw.logger.Error("Max retries reached; giving up",
				"chain", rw.chain.GetName(), "block", block)
		} else {
			rw.failedBlocks[block] = count + 1
			rw.addSave(block)
		}
	}
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

func (rw *RescannerWorker) processBatch(blocks []uint64) error {
	batchSize := 20
	if len(blocks) > batchSize {
		blocks = blocks[:batchSize]
	}

	results, err := rw.chain.GetBlocksByNumbers(rw.ctx, blocks)
	if err != nil {
		return fmt.Errorf("rescanner get blocks: %w", err)
	}

	var toRemove []uint64
	success := 0

	for _, res := range results {
		if rw.handleBlockResult(res) {
			success++
			toRemove = append(toRemove, res.Number)
		} else {
			rw.incrementRetry(res.Number)
		}
	}
	if len(toRemove) > 0 {
		rw.removeBlocks(toRemove)
	}

	rw.logger.Info("Rescanner pass",
		"chain", rw.chain.GetName(),
		"retried", len(blocks),
		"success", success,
		"remaining", len(rw.failedBlocks),
	)
	return nil
}

// batching helpers
func (rw *RescannerWorker) addSave(blocks ...uint64) {
	rw.batchMu.Lock()
	defer rw.batchMu.Unlock()
	rw.pendingSaves = append(rw.pendingSaves, blocks...)
	if len(rw.pendingSaves) >= BatchFlushSize {
		rw.flushUnsafe()
	}
}

func (rw *RescannerWorker) addRemove(blocks ...uint64) {
	rw.batchMu.Lock()
	defer rw.batchMu.Unlock()
	rw.pendingRemoves = append(rw.pendingRemoves, blocks...)
	if len(rw.pendingRemoves) >= BatchFlushSize {
		rw.flushUnsafe()
	}
}

func (rw *RescannerWorker) flush() {
	rw.batchMu.Lock()
	defer rw.batchMu.Unlock()
	rw.flushUnsafe()
}

func (rw *RescannerWorker) flushUnsafe() {
	if len(rw.pendingSaves) > 0 {
		_ = rw.blockStore.SaveFailedBlocks(rw.chain.GetNetworkInternalCode(), rw.pendingSaves)
		rw.logger.Debug("Batch saved failed blocks",
			"chain", rw.chain.GetName(),
			"count", len(rw.pendingSaves))
		rw.pendingSaves = rw.pendingSaves[:0]
	}
	if len(rw.pendingRemoves) > 0 {
		_ = rw.blockStore.RemoveFailedBlocks(rw.chain.GetNetworkInternalCode(), rw.pendingRemoves)
		rw.logger.Debug("Batch removed failed blocks",
			"chain", rw.chain.GetName(),
			"count", len(rw.pendingRemoves))
		rw.pendingRemoves = rw.pendingRemoves[:0]
	}
}
