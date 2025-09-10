package worker

import (
	"context"
	"time"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/missingblockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
)

type BlockScannerConfig struct {
	MaxBlocksPerScan int
	DurationPerScan  time.Duration
	// Delay between each block scan to avoid rate limiting
	DelayPerIteration time.Duration
	DelayPerScan      time.Duration
	// Offset is the number of blocks to scan backward from the last block
	Offset uint64
	// MaxBlockDiffToResetCurrentBlock represents the maximum difference between the current block and the latest block.
	// If the difference exceeds this value, the current block will be reset.
	// This is to ensure the blockchain scanner does not fall too far behind the latest block.
	MaxBlockDiffToResetCurrentBlock uint64
	IsRealTime                      bool
}

type ManualWorker struct {
	*BaseWorker
	blockScannerConfig BlockScannerConfig
	mbs                missingblockstore.MissingBlocksStore
}

func NewManualWorker(ctx context.Context, chain indexer.Indexer, config config.ChainConfig, kv infra.KVStore, redisClient infra.RedisClient, blockStore *blockstore.Store, emitter *events.Emitter, pubkeyStore pubkeystore.Store, failedChan chan FailedBlockEvent) *ManualWorker {
	worker := newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, pubkeyStore, ModeManual, failedChan)
	manualWorker := &ManualWorker{
		BaseWorker: worker,
		mbs:        missingblockstore.NewMissingBlocksStore(redisClient),
	}
	return manualWorker
}
func (mw *ManualWorker) Start() {
	mw.logger.Info("Starting manual worker", "chain", mw.chain.GetName())
	go mw.run(mw.processManualBlocks)
}

func (mw *ManualWorker) processManualBlocks() error {
	return nil
}
