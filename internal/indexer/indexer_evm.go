package indexer

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/rpc"
	"github.com/fystack/transaction-indexer/internal/rpc/evm"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/common/utils"
	"golang.org/x/sync/errgroup"
)

type EVMIndexer struct {
	config              config.ChainConfig
	failover            *rpc.Failover[evm.EthereumAPI]
	maxBatchSize        int // Maximum batch size to prevent RPC timeouts
	maxReceiptBatchSize int // Specific limit for receipt batches (usually smaller)
}

func NewEVMIndexer(config config.ChainConfig, failover *rpc.Failover[evm.EthereumAPI]) *EVMIndexer {
	maxBatchSize := 20 // Default max batch size for blocks
	if config.Throttle.BatchSize > 0 && config.Throttle.BatchSize < maxBatchSize {
		maxBatchSize = config.Throttle.BatchSize
	}

	// Receipt batches need to be smaller due to RPC provider limits
	maxReceiptBatchSize := 25 // Conservative limit that works with most providers
	if config.Throttle.BatchSize > 0 && config.Throttle.BatchSize < maxReceiptBatchSize {
		maxReceiptBatchSize = config.Throttle.BatchSize
	}

	return &EVMIndexer{
		config:              config,
		failover:            failover,
		maxBatchSize:        maxBatchSize,
		maxReceiptBatchSize: maxReceiptBatchSize,
	}
}

func (e *EVMIndexer) GetName() string                  { return strings.ToUpper(e.config.Name) }
func (e *EVMIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeEVM }

func (e *EVMIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
		n, err := c.GetBlockNumber(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (e *EVMIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	results, err := e.fetchBlocks(ctx, []uint64{number}, false)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 || results[0].Error != nil {
		if len(results) > 0 && results[0].Error != nil {
			return nil, fmt.Errorf("block error: %s", results[0].Error.Message)
		}
		return nil, fmt.Errorf("block not found")
	}

	return results[0].Block, nil
}

func (e *EVMIndexer) GetBlocks(
	ctx context.Context,
	from, to uint64,
	isParallel bool,
) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range")
	}

	blockNums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		blockNums = append(blockNums, n)
	}

	return e.fetchBlocks(ctx, blockNums, isParallel)
}

func (e *EVMIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	return e.fetchBlocks(ctx, blockNumbers, false)
}

// Unified block fetching logic
func (e *EVMIndexer) fetchBlocks(
	ctx context.Context,
	blockNums []uint64,
	isParallel bool,
) ([]BlockResult, error) {
	if len(blockNums) == 0 {
		return nil, nil
	}

	// Fetch raw blocks
	blocks, err := e.getRawBlocks(ctx, blockNums, isParallel)
	if err != nil {
		return e.fallbackIndividual(ctx, blockNums)
	}

	// Process blocks and fetch receipts
	return e.processBlocksAndReceipts(ctx, blockNums, blocks, isParallel)
}

// Simplified raw block fetching
func (e *EVMIndexer) getRawBlocks(
	ctx context.Context,
	blockNums []uint64,
	isParallel bool,
) (map[uint64]*evm.Block, error) {
	if isParallel {
		return e.getRawBlocksParallel(ctx, blockNums)
	}

	// Sequential processing with batch size limits
	return e.getRawBlocksSequential(ctx, blockNums)
}

func (e *EVMIndexer) getRawBlocksParallel(
	ctx context.Context,
	blockNums []uint64,
) (map[uint64]*evm.Block, error) {
	providers := e.failover.GetAvailableProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no available providers")
	}

	// First split by providers
	providerChunks := utils.ChunkBySize(blockNums, len(providers))
	var (
		mu     sync.Mutex
		blocks = make(map[uint64]*evm.Block)
	)

	var g errgroup.Group
	for i, chunk := range providerChunks {
		if len(chunk) == 0 {
			continue
		}

		provider := providers[i]
		g.Go(func() error {
			// Further split each provider's chunk into smaller batches
			batches := utils.ChunkBySize(chunk, (len(chunk)+e.maxBatchSize-1)/e.maxBatchSize)

			for _, batch := range batches {
				var subBlocks map[uint64]*evm.Block

				// Try with the assigned provider first
				err := e.failover.ExecuteWithRetryProvider(
					ctx,
					provider,
					func(c evm.EthereumAPI) error {
						var err error
						subBlocks, err = c.BatchGetBlocksByNumber(ctx, batch, true)
						return err
					},
				)

				if err != nil {
					// Try with all providers as fallback
					logger.Warn("provider-specific batch failed, trying with failover",
						"provider", provider, "error", err, "batch_size", len(batch))

					err = e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
						var err error
						subBlocks, err = c.BatchGetBlocksByNumber(ctx, batch, true)
						return err
					})

					if err != nil {
						// Final fallback to individual blocks
						logger.Warn("all providers failed for batch, falling back to individual",
							"error", err, "batch_size", len(batch))

						individualBlocks, fallbackErr := e.fallbackBatchToIndividual(ctx, batch)
						if fallbackErr != nil {
							return fmt.Errorf("all fallback methods failed: %w", fallbackErr)
						}
						subBlocks = individualBlocks
					}
				}

				mu.Lock()
				maps.Copy(blocks, subBlocks)
				mu.Unlock()
			}
			return nil
		})
	}

	return blocks, g.Wait()
}

func (e *EVMIndexer) getRawBlocksSequential(
	ctx context.Context,
	blockNums []uint64,
) (map[uint64]*evm.Block, error) {
	allBlocks := make(map[uint64]*evm.Block)

	// Split into manageable batches
	batches := utils.ChunkBySize(blockNums, (len(blockNums)+e.maxBatchSize-1)/e.maxBatchSize)

	for _, batch := range batches {
		var blocks map[uint64]*evm.Block

		// Try with failover across all providers
		err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
			var err error
			blocks, err = c.BatchGetBlocksByNumber(ctx, batch, true)
			return err
		})

		if err != nil {
			// Fallback to individual block fetching for this batch
			logger.Warn("batch failed, falling back to individual blocks",
				"error", err, "batch_size", len(batch))

			individualBlocks, fallbackErr := e.fallbackBatchToIndividual(ctx, batch)
			if fallbackErr != nil {
				return allBlocks, fmt.Errorf(
					"batch and individual fallback failed: %w",
					fallbackErr,
				)
			}
			maps.Copy(allBlocks, individualBlocks)
		} else {
			maps.Copy(allBlocks, blocks)
		}
	}

	return allBlocks, nil
}

// Unified block processing and receipt fetching
func (e *EVMIndexer) processBlocksAndReceipts(
	ctx context.Context,
	blockNums []uint64,
	blocks map[uint64]*evm.Block,
	isParallel bool,
) ([]BlockResult, error) {
	// Handle missing blocks by fetching them individually
	missingNums := e.findMissingBlocks(blockNums, blocks)
	if len(missingNums) > 0 {
		missingBlocks, err := e.fetchMissingBlocksRaw(ctx, missingNums)
		if err != nil {
			logger.Warn("failed to fetch missing blocks", "error", err, "count", len(missingNums))
		} else {
			maps.Copy(blocks, missingBlocks)
		}
	}

	// Extract transaction hashes that need receipts
	txHashMap := e.extractReceiptTxHashes(blocks)

	// Fetch all receipts at once
	allReceipts, err := e.fetchAllReceipts(ctx, txHashMap, isParallel)
	if err != nil {
		logger.Warn("failed to fetch receipts", "error", err)
	}

	// Build final results
	return e.buildBlockResults(blockNums, blocks, txHashMap, allReceipts), nil
}

// fetchMissingBlocksRaw fetches missing blocks as raw evm.Block objects
func (e *EVMIndexer) fetchMissingBlocksRaw(
	ctx context.Context,
	blockNums []uint64,
) (map[uint64]*evm.Block, error) {
	results := make(map[uint64]*evm.Block)

	for _, num := range blockNums {
		var eb *evm.Block
		hexNum := fmt.Sprintf("0x%x", num)

		err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
			var err error
			eb, err = c.GetBlockByNumber(ctx, hexNum, true)
			return err
		})

		if err != nil {
			logger.Warn("failed to fetch individual block", "block_num", num, "error", err)
			continue
		}

		results[num] = eb
	}

	return results, nil
}

func (e *EVMIndexer) extractReceiptTxHashes(blocks map[uint64]*evm.Block) map[uint64][]string {
	txHashMap := make(map[uint64][]string)
	for blockNum, block := range blocks {
		if block == nil {
			continue
		}
		for _, tx := range block.Transactions {
			if tx.NeedReceipt() {
				txHashMap[blockNum] = append(txHashMap[blockNum], tx.Hash)
			}
		}
	}
	return txHashMap
}

func (e *EVMIndexer) fetchAllReceipts(
	ctx context.Context,
	txHashMap map[uint64][]string,
	isParallel bool,
) (map[string]*evm.TxnReceipt, error) {
	// Flatten all tx hashes
	var allTxHashes []string
	for _, hashes := range txHashMap {
		allTxHashes = append(allTxHashes, hashes...)
	}

	if len(allTxHashes) == 0 {
		return nil, nil
	}

	if isParallel {
		return e.fetchReceiptsParallel(ctx, allTxHashes)
	}
	return e.fetchReceiptsSequential(ctx, allTxHashes)
}

func (e *EVMIndexer) fetchReceiptsSequential(
	ctx context.Context,
	txHashes []string,
) (map[string]*evm.TxnReceipt, error) {
	allReceipts := make(map[string]*evm.TxnReceipt)

	// Use the specific receipt batch size limit
	batches := utils.ChunkBySize(txHashes, e.maxReceiptBatchSize)

	for batchIdx, batch := range batches {
		var receipts map[string]*evm.TxnReceipt

		logger.Debug(
			"fetching receipt batch",
			"batch",
			batchIdx+1,
			"total_batches",
			len(batches),
			"batch_size",
			len(batch),
		)

		// Try with failover across all providers
		err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
			var err error
			receipts, err = c.BatchGetTransactionReceipts(ctx, batch)
			return err
		})

		if err != nil {
			logger.Warn(
				"receipt batch failed",
				"error",
				err,
				"batch_size",
				len(batch),
				"batch_idx",
				batchIdx,
			)
			return allReceipts, err
		}

		maps.Copy(allReceipts, receipts)
	}

	return allReceipts, nil
}

func (e *EVMIndexer) fetchReceiptsParallel(
	ctx context.Context,
	txHashes []string,
) (map[string]*evm.TxnReceipt, error) {
	providers := e.failover.GetAvailableProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no available providers")
	}

	providerChunks := utils.ChunkBySize(txHashes, len(providers))
	var (
		mu       sync.Mutex
		receipts = make(map[string]*evm.TxnReceipt)
		errs     types.MultiError
	)

	var g errgroup.Group
	for i, chunk := range providerChunks {
		if len(chunk) == 0 {
			continue
		}

		provider := providers[i]
		providerIdx := i

		g.Go(func() error {
			batches := utils.ChunkBySize(
				chunk,
				(len(chunk)+e.maxReceiptBatchSize-1)/e.maxReceiptBatchSize,
			)

			for batchIdx, batch := range batches {
				var subReceipts map[string]*evm.TxnReceipt

				err := e.failover.ExecuteWithRetryProvider(
					ctx,
					provider,
					func(c evm.EthereumAPI) error {
						var err error
						subReceipts, err = c.BatchGetTransactionReceipts(ctx, batch)
						return err
					},
				)

				if err != nil {
					logger.Warn("receipt batch failed",
						"chain", e.GetName(),
						"provider_idx", providerIdx,
						"batch_idx", batchIdx+1,
						"batch_size", len(batch),
						"error", err,
					)
					errs.Add(fmt.Errorf("provider %s batch %d: %w", provider.Name, batchIdx+1, err))
					continue
				}

				mu.Lock()
				maps.Copy(receipts, subReceipts)
				mu.Unlock()
			}
			return nil
		})
	}

	_ = g.Wait()

	if !errs.IsEmpty() {
		return receipts, &errs
	}
	return receipts, nil
}

func (e *EVMIndexer) buildBlockResults(
	blockNums []uint64,
	blocks map[uint64]*evm.Block,
	txHashMap map[uint64][]string,
	allReceipts map[string]*evm.TxnReceipt,
) []BlockResult {
	results := make([]BlockResult, 0, len(blockNums))

	for _, num := range blockNums {
		block := blocks[num]
		if block == nil {
			results = append(results, BlockResult{
				Number: num,
				Error:  &Error{ErrorType: ErrorTypeUnknown, Message: "block not found"},
			})
			continue
		}

		// Build receipts map for this block
		blockReceipts := make(map[string]*evm.TxnReceipt)
		for _, txHash := range txHashMap[num] {
			if receipt := allReceipts[txHash]; receipt != nil {
				blockReceipts[txHash] = receipt
			}
		}

		typesBlock, err := e.convertBlock(block, blockReceipts)
		if err != nil {
			results = append(results, BlockResult{
				Number: num,
				Error:  &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
		} else {
			results = append(results, BlockResult{Number: num, Block: typesBlock})
		}
	}

	return results
}

func (e *EVMIndexer) fallbackIndividual(
	ctx context.Context,
	blockNums []uint64,
) ([]BlockResult, error) {
	results := make([]BlockResult, 0, len(blockNums))

	for _, num := range blockNums {
		var eb *evm.Block
		hexNum := fmt.Sprintf("0x%x", num)

		err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
			var err error
			eb, err = c.GetBlockByNumber(ctx, hexNum, true)
			return err
		})

		if err != nil {
			results = append(results, BlockResult{
				Number: num,
				Error:  &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
			continue
		}

		// Fetch receipts for this block
		var needReceiptTxs []string
		for _, tx := range eb.Transactions {
			if tx.NeedReceipt() {
				needReceiptTxs = append(needReceiptTxs, tx.Hash)
			}
		}

		receipts := make(map[string]*evm.TxnReceipt)
		if len(needReceiptTxs) > 0 {
			_ = e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
				r, err := c.BatchGetTransactionReceipts(ctx, needReceiptTxs)
				if err == nil && r != nil {
					receipts = r
				}
				return nil
			})
		}

		block, err := e.convertBlock(eb, receipts)
		if err != nil {
			results = append(results, BlockResult{
				Number: num,
				Error:  &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
		} else {
			results = append(results, BlockResult{Number: num, Block: block})
		}
	}

	return results, nil
}

// fallbackBatchToIndividual fetches blocks individually when batch operations fail
func (e *EVMIndexer) fallbackBatchToIndividual(
	ctx context.Context,
	blockNums []uint64,
) (map[uint64]*evm.Block, error) {
	blocks := make(map[uint64]*evm.Block)
	var errs types.MultiError

	for _, num := range blockNums {
		var eb *evm.Block
		hexNum := fmt.Sprintf("0x%x", num)

		err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
			var err error
			eb, err = c.GetBlockByNumber(ctx, hexNum, true)
			return err
		})

		if err != nil {
			logger.Warn("individual block fetch failed",
				"chain", e.GetName(),
				"block", num,
				"error", err,
			)
			errs.Add(fmt.Errorf("block %d: %w", num, err))
			continue
		}

		blocks[num] = eb
	}

	if !errs.IsEmpty() {
		return blocks, &errs
	}
	return blocks, nil
}

func (e *EVMIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.GetLatestBlockNumber(ctx)
	return err == nil
}

func (e *EVMIndexer) convertBlock(
	eb *evm.Block,
	receipts map[string]*evm.TxnReceipt,
) (*types.Block, error) {
	num, _ := utils.ParseHexUint64(eb.Number)
	ts, _ := utils.ParseHexUint64(eb.Timestamp)

	var allTransfers []types.Transaction
	for _, tx := range eb.Transactions {
		receipt := receipts[tx.Hash]
		transfers := tx.ExtractTransfers(e.GetName(), receipt, num, ts)
		allTransfers = append(allTransfers, transfers...)
	}

	return &types.Block{
		Number:       num,
		Hash:         eb.Hash,
		ParentHash:   eb.ParentHash,
		Timestamp:    ts,
		Transactions: allTransfers,
	}, nil
}

// Helper functions for cleaner code
func (e *EVMIndexer) findMissingBlocks(blockNums []uint64, blocks map[uint64]*evm.Block) []uint64 {
	var missing []uint64
	for _, num := range blockNums {
		if blocks[num] == nil {
			missing = append(missing, num)
		}
	}
	return missing
}
