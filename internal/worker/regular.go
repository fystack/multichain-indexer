package worker

import (
	"context"
	"fmt"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
)

const MaxBlockHashSize = 20

type RegularWorker struct {
	*BaseWorker
	currentBlock uint64
	blockHashes  []BlockHashEntry
}

type BlockHashEntry struct {
	BlockNumber uint64
	Hash        string
}

func NewRegularWorker(ctx context.Context, deps WorkerDeps) *RegularWorker {
	worker := newWorkerWithMode(ctx, deps, ModeRegular)
	rw := &RegularWorker{BaseWorker: worker}
	rw.currentBlock = rw.determineStartingBlock()
	rw.blockHashes = make([]BlockHashEntry, 0, MaxBlockHashSize)
	return rw
}

func (rw *RegularWorker) Start() {
	rw.logger.Info("Starting regular worker",
		"chain", rw.deps.Chain.GetName(),
		"start_block", rw.currentBlock,
	)
	go rw.run(rw.processRegularBlocks)
}

func (rw *RegularWorker) Stop() {
	if rw.currentBlock > 0 {
		_ = rw.deps.BlockStore.SaveLatestBlock(rw.deps.Chain.GetNetworkInternalCode(), rw.currentBlock)
	}
	rw.clearBlockHashes()
	rw.BaseWorker.Stop()
}

func (rw *RegularWorker) processRegularBlocks() error {
	latest, err := rw.resolveLatestBlock()
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}

	if rw.currentBlock > latest {
		if rw.deps.Chain.GetNetworkType() == enum.NetworkTypeBtc {
			rw.logger.Info("Waiting for confirmations", "current", rw.currentBlock, "confirmed_height", latest)
		} else {
			rw.logger.Info("Waiting for new blocks", "current", rw.currentBlock, "latest", latest)
		}
		return nil
	}

	batchSize := uint64(rw.deps.Config.Throttle.BatchSize)
	if batchSize == 0 {
		batchSize = 1
	}
	end := min(rw.currentBlock+batchSize-1, latest)

	rw.logger.Info("Processing range",
		"chain", rw.deps.Chain.GetName(),
		"start", rw.currentBlock,
		"end", end,
		"size", end-rw.currentBlock+1,
	)

	originalStart := rw.currentBlock
	originalEnd := end

	results, err := rw.deps.Chain.GetBlocks(rw.ctx, rw.currentBlock, end, rw.deps.Config.Throttle.Parallel)
	if err != nil {
		return fmt.Errorf("get blocks: %w", err)
	}

	lastSuccess, lastSuccessHash, reorg, err := rw.processBlockResults(results)
	if err != nil {
		return err
	}
	if reorg {
		return nil
	}

	rw.advanceLatestBlock(lastSuccess, lastSuccessHash)

	rw.logger.Info("Processed latest blocks",
		"chain", rw.deps.Chain.GetName(),
		"start", originalStart,
		"end", originalEnd,
		"last_success", lastSuccess,
		"expected", originalEnd-originalStart+1,
		"got", len(results),
	)
	return nil
}

func (rw *RegularWorker) determineStartingBlock() uint64 {
	chainLatest, err1 := rw.deps.Chain.GetLatestBlockNumber(rw.ctx)
	kvLatest, err2 := rw.deps.BlockStore.GetLatestBlock(rw.deps.Chain.GetNetworkInternalCode())

	if err1 != nil && err2 != nil {
		rw.logger.Warn("Cannot get latest block from chain or KV, using config.StartBlock",
			"chain", rw.deps.Chain.GetName(),
			"start_block", rw.deps.Config.StartBlock,
		)
		return uint64(rw.deps.Config.StartBlock)
	}

	if err1 != nil && kvLatest > 0 {
		rw.logger.Warn("Chain RPC failed, resuming from KV latest",
			"chain", rw.deps.Chain.GetName(),
			"kv_latest", kvLatest,
		)
		return kvLatest
	}

	if err2 != nil || kvLatest == 0 {
		if rw.deps.Config.FromLatest {
			rw.logger.Info("No KV checkpoint found, starting from latest chain block",
				"chain", rw.deps.Chain.GetName(),
				"latest", chainLatest,
			)
			return chainLatest
		}

		rw.logger.Info("No KV checkpoint found, starting from configured start block",
			"chain", rw.deps.Chain.GetName(),
			"start_block", rw.deps.Config.StartBlock,
		)
		return uint64(rw.deps.Config.StartBlock)
	}

	if chainLatest > kvLatest {
		start := kvLatest + 1
		end := chainLatest
		ranges := splitCatchupRange(blockstore.CatchupRange{Start: start, End: end, Current: start - 1}, defaultCatchupRangeSize)

		for _, r := range ranges {
			_ = rw.deps.BlockStore.SaveCatchupProgress(
				rw.deps.Chain.GetNetworkInternalCode(),
				r.Start,
				r.End,
				r.Current,
			)
		}

		rw.logger.Info("Queued catchup ranges",
			"chain", rw.deps.Chain.GetName(),
			"gap", formatRange(start, end),
			"ranges_created", len(ranges),
		)
	}

	return chainLatest
}

func (rw *RegularWorker) detectAndHandleReorg(res *indexer.BlockResult) (bool, error) {
	prevNum := res.Block.Number - 1
	storedHash := rw.getBlockHash(prevNum)
	if storedHash == "" || storedHash == res.Block.ParentHash {
		return false, nil
	}

	rollbackWindow := uint64(rw.deps.Config.ReorgRollbackWindow)
	if rollbackWindow == 0 {
		rollbackWindow = constant.DefaultReorgRollbackWindow
	}

	reorgStart := uint64(1)
	if prevNum > rollbackWindow {
		reorgStart = prevNum - rollbackWindow
	}

	rw.logger.Warn("Reorg detected; rolling back",
		"chain", rw.deps.Chain.GetName(),
		"at_block", prevNum,
		"expected_parent", storedHash,
		"actual_parent", res.Block.ParentHash,
		"rollback_start", reorgStart,
		"rollback_end", prevNum,
	)

	rw.clearBlockHashes()
	if err := rw.deps.BlockStore.SaveLatestBlock(rw.deps.Chain.GetNetworkInternalCode(), reorgStart-1); err != nil {
		return true, fmt.Errorf("save latest block: %w", err)
	}
	rw.currentBlock = reorgStart
	return true, nil
}

func (rw *RegularWorker) isReorgCheckRequired() bool {
	networkType := rw.deps.Chain.GetNetworkType()
	return networkType == enum.NetworkTypeEVM || networkType == enum.NetworkTypeBtc
}

func (rw *RegularWorker) addBlockHash(blockNumber uint64, hash string) {
	rw.blockHashes = append(rw.blockHashes, BlockHashEntry{BlockNumber: blockNumber, Hash: hash})
	if len(rw.blockHashes) > MaxBlockHashSize {
		rw.blockHashes = rw.blockHashes[len(rw.blockHashes)-MaxBlockHashSize:]
	}
}

func (rw *RegularWorker) getBlockHash(blockNumber uint64) string {
	for _, entry := range rw.blockHashes {
		if entry.BlockNumber == blockNumber {
			return entry.Hash
		}
	}
	return ""
}

func (rw *RegularWorker) clearBlockHashes() {
	rw.blockHashes = rw.blockHashes[:0]
}

func checkContinuity(prev, curr indexer.BlockResult) bool {
	return prev.Block.Hash == curr.Block.ParentHash
}

func (rw *RegularWorker) resolveLatestBlock() (uint64, error) {
	latest, err := rw.deps.Chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return 0, err
	}

	if rw.deps.Chain.GetNetworkType() != enum.NetworkTypeBtc {
		return latest, nil
	}

	btcIndexer, ok := rw.deps.Chain.(*indexer.BitcoinIndexer)
	if !ok {
		return latest, nil
	}

	confirmedHeight, err := btcIndexer.GetConfirmedHeight(rw.ctx)
	if err != nil {
		return 0, fmt.Errorf("get confirmed height: %w", err)
	}

	rw.logger.Info("Using Bitcoin confirmed height",
		"latest", latest,
		"confirmed", confirmedHeight,
		"current", rw.currentBlock,
	)
	return confirmedHeight, nil
}

func (rw *RegularWorker) processBlockResults(results []indexer.BlockResult) (uint64, string, bool, error) {
	lastSuccess := rw.currentBlock - 1
	lastSuccessHash := ""
	reorgCheck := rw.isReorgCheckRequired()

	for i, res := range results {
		if res.Block == nil || res.Error != nil {
			continue
		}

		if reorgCheck {
			reorg, err := rw.detectAndHandleReorg(&res)
			if err != nil {
				return lastSuccess, lastSuccessHash, false, err
			}
			if reorg {
				return lastSuccess, lastSuccessHash, true, nil
			}
		}

		if i > 0 && reorgCheck {
			prev := results[i-1]
			if prev.Block == nil || prev.Error != nil {
				rw.logger.Warn("Previous block in batch is invalid, retrying next tick",
					"prev_index", i-1,
					"curr", res.Block.Number,
				)
				break
			}
			if !checkContinuity(prev, res) {
				rw.logger.Warn("Batch continuity broken, retrying next tick",
					"prev", prev.Block.Number,
					"prev_hash", prev.Block.Hash,
					"curr", res.Block.Number,
					"curr_parent", res.Block.ParentHash,
				)
				break
			}
		}

		if rw.handleBlockResult(res) {
			lastSuccess = res.Number
			lastSuccessHash = res.Block.Hash
		}
	}

	return lastSuccess, lastSuccessHash, false, nil
}

func (rw *RegularWorker) advanceLatestBlock(lastSuccess uint64, lastSuccessHash string) {
	if lastSuccess < rw.currentBlock {
		return
	}

	rw.currentBlock = lastSuccess + 1
	_ = rw.deps.BlockStore.SaveLatestBlock(rw.deps.Chain.GetNetworkInternalCode(), lastSuccess)
	if lastSuccessHash != "" {
		rw.addBlockHash(lastSuccess, lastSuccessHash)
	}
}
