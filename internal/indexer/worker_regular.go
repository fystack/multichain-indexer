package indexer

import (
	"context"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/pubkeystore"
)

type RegularWorker struct {
	*BaseWorker
}

// NewWorker creates a worker for regular indexing
func NewRegularWorker(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, pubkeyStore pubkeystore.Store, failedChan chan FailedBlockEvent) *RegularWorker {
	worker := newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, pubkeyStore, ModeRegular, failedChan)
	regular := &RegularWorker{
		BaseWorker: worker,
	}
	regular.currentBlock = regular.determineStartingBlock()
	return regular
}

// Start the worker
func (rw *RegularWorker) Start() {
	rw.logger.Info("Starting regular worker", "chain", rw.chain.GetName(), "start_block", rw.currentBlock)
	go rw.run(rw.processRegularBlocks)
}

func (rw *RegularWorker) processRegularBlocks() error {
	latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	if rw.currentBlock > latest {
		time.Sleep(3 * time.Second)
		return nil
	}

	end := min(rw.currentBlock+uint64(rw.config.BatchSize)-1, latest)
	lastSuccess := rw.currentBlock - 1

	start := time.Now()
	results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end)
	if err != nil {
		return fmt.Errorf("get batch blocks: %w", err)
	}
	elapsed := time.Since(start)
	rw.logger.Info(
		"Processing latest blocks",
		"chain",
		rw.chain.GetName(),
		"start",
		rw.currentBlock,
		"end",
		end,
		"elapsed",
		elapsed,
		"last_success",
		lastSuccess,
		"expected number of blocks",
		end-rw.currentBlock+1,
		"actual blocks",
		len(results),
	)

	for _, result := range results {
		if rw.handleBlockResult(result) {
			lastSuccess = result.Number
		}
	}

	if lastSuccess >= rw.currentBlock {
		rw.currentBlock = lastSuccess + 1
		_ = rw.blockStore.SaveLatestBlock(rw.chain.GetName(), lastSuccess)
	}
	return nil
}

// determineStartingBlock determines the starting block for the regular worker
// it will use the latest block from the block store if it exists, otherwise it will use the start block from the config
// if from latest is true, it will use the latest block from the chain
func (rw *RegularWorker) determineStartingBlock() uint64 {
	latestBlock, err := rw.blockStore.GetLatestBlock(rw.chain.GetName())
	if err != nil || latestBlock == 0 {
		latestBlock = uint64(rw.config.StartBlock)
	}
	if rw.config.FromLatest {
		chainLatest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
		if err == nil {
			latestBlock = chainLatest
		}
	}
	return latestBlock
}
