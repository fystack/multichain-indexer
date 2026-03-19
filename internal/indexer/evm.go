package indexer

import (
	"context"
	"fmt"
	"maps"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
	"golang.org/x/sync/errgroup"
)

type EVMIndexer struct {
	chainName           string
	config              config.ChainConfig
	failover            *rpc.Failover[evm.EthereumAPI]
	traceFailover       *rpc.Failover[evm.EthereumAPI] // nil if no debug-capable nodes
	maxBatchSize        int                             // Maximum batch size to prevent RPC timeouts
	maxReceiptBatchSize int                             // Specific limit for receipt batches (usually smaller)
	pubkeyStore         PubkeyStore                     // For selective receipt fetching
}

// traceModeActive returns true when tracing can actually run right now.
// Called once per batch in processBlocksAndReceipts (not stored as a field)
// because provider availability is runtime state — all trace providers may
// be blacklisted after startup.
// The result is passed to extractReceiptTxHashes to avoid repeated calls
// to GetAvailableProviders() in the hot loop.
// TraceModeActive is the exported version for testing from other packages.
func (e *EVMIndexer) TraceModeActive() bool {
	return e.traceModeActive()
}

func (e *EVMIndexer) traceModeActive() bool {
	return e.config.DebugTrace && e.traceFailover != nil &&
		len(e.traceFailover.GetAvailableProviders()) > 0
}

// PubkeyStore interface for checking if an address is monitored
type PubkeyStore interface {
	Exist(addressType enum.NetworkType, address string) bool
}

func NewEVMIndexer(chainName string, config config.ChainConfig, failover *rpc.Failover[evm.EthereumAPI], traceFailover *rpc.Failover[evm.EthereumAPI], pubkeyStore PubkeyStore) *EVMIndexer {
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
		chainName:           chainName,
		config:              config,
		failover:            failover,
		traceFailover:       traceFailover,
		maxBatchSize:        maxBatchSize,
		maxReceiptBatchSize: maxReceiptBatchSize,
		pubkeyStore:         pubkeyStore,
	}
}

func (e *EVMIndexer) GetName() string                  { return strings.ToUpper(e.chainName) }
func (e *EVMIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeEVM }
func (e *EVMIndexer) GetNetworkInternalCode() string {
	return e.config.InternalCode
}
func (e *EVMIndexer) GetNetworkId() string {
	return e.config.NetworkId
}

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

		providerIdx := i % len(providers)
		provider := providers[providerIdx]
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
					true,
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
	startTime := time.Now()

	traceActive := e.traceModeActive() // single snapshot per batch

	var (
		erc20TxHashes map[string]bool
		missingBlocks map[uint64]*evm.Block
	)

	g, gctx := errgroup.WithContext(ctx)

	missingNums := e.findMissingBlocks(blockNums, blocks)
	if len(missingNums) > 0 {
		g.Go(func() error {
			var err error
			missingBlocks, err = e.fetchMissingBlocksRaw(gctx, missingNums)
			if err != nil {
				logger.Warn("failed to fetch missing blocks", "error", err, "count", len(missingNums))
				// Don't fail the entire operation, just log
				return nil
			}
			return nil
		})
	}

	if len(blockNums) > 0 && e.pubkeyStore != nil && !traceActive {
		g.Go(func() error {
			fromBlock := blockNums[0]
			toBlock := blockNums[len(blockNums)-1]

			erc20Start := time.Now()
			matchedTxHashes, err := e.queryERC20TransfersToMonitoredAddresses(gctx, fromBlock, toBlock)
			if err != nil {
				logger.Warn("failed to query ERC20 transfers", "error", err)
				// Don't fail the entire operation, just log
				return nil
			}
			erc20TxHashes = matchedTxHashes
			logger.Info("[ERC20 QUERY COMPLETE]",
				"elapsed_ms", time.Since(erc20Start).Milliseconds(),
				"matched_txs", len(erc20TxHashes),
			)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		logger.Warn("parallel operations failed", "error", err)
	}

	if missingBlocks != nil && len(missingBlocks) > 0 {
		maps.Copy(blocks, missingBlocks)
		logger.Info("[MISSING BLOCKS FETCHED]", "count", len(missingBlocks))
	}

	// Extract transaction hashes for native transfers to monitored addresses
	txHashMap := e.extractReceiptTxHashes(blocks, traceActive)
	// Add ERC20 transfer tx hashes to the map
	if len(erc20TxHashes) > 0 {
		// Use a set to track already added hashes for efficient deduplication
		addedHashes := make(map[string]bool)
		for _, hashes := range txHashMap {
			for _, hash := range hashes {
				addedHashes[hash] = true
			}
		}

		for blockNum, block := range blocks {
			if block == nil {
				continue
			}
			for _, tx := range block.Transactions {
				if erc20TxHashes[tx.Hash] && !addedHashes[tx.Hash] {
					txHashMap[blockNum] = append(txHashMap[blockNum], tx.Hash)
					addedHashes[tx.Hash] = true
				}
			}
		}
	}

	// Fetch all receipts at once
	receiptsStart := time.Now()
	allReceipts, err := e.fetchAllReceipts(ctx, txHashMap, isParallel)
	if err != nil {
		logger.Warn("failed to fetch receipts", "error", err)
	}
	logger.Debug("[RECEIPTS FETCHED]",
		"elapsed_ms", time.Since(receiptsStart).Milliseconds(),
		"count", len(allReceipts),
	)

	// Fetch traces for successful contract calls
	var traces map[string]*evm.CallTrace
	if traceActive {
		tracesStart := time.Now()
		traces = e.fetchTraces(ctx, blocks, allReceipts)
		logger.Debug("[TRACES FETCHED]",
			"elapsed_ms", time.Since(tracesStart).Milliseconds(),
			"count", len(traces))
	}

	totalElapsed := time.Since(startTime)
	logger.Debug("[PROCESS BLOCKS COMPLETE]",
		"total_elapsed_ms", totalElapsed.Milliseconds(),
		"blocks", len(blockNums),
	)

	// Build final results
	return e.buildBlockResults(blockNums, blocks, txHashMap, allReceipts, traces), nil
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

// queryERC20TransfersToMonitoredAddresses queries Transfer events and returns tx hashes for matched addresses
func (e *EVMIndexer) queryERC20TransfersToMonitoredAddresses(ctx context.Context, fromBlock, toBlock uint64) (map[string]bool, error) {
	if e.pubkeyStore == nil {
		return nil, nil
	}

	// Transfer(address,address,uint256) event signature
	// Keccak256 hash: 0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef
	transferSignature := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	query := evm.FilterQuery{
		FromBlock: fmt.Sprintf("0x%x", fromBlock),
		ToBlock:   fmt.Sprintf("0x%x", toBlock),
		Topics:    [][]string{{transferSignature}}, // Filter by Transfer event signature
	}

	var logs []evm.Log
	err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
		var err error
		logs, err = c.FilterLogs(ctx, query)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query Transfer logs: %w", err)
	}

	// Map of tx hashes that have transfers involving monitored addresses.
	// When two_way_indexing is enabled, both 'to' and 'from' sides are considered.
	matchedTxHashes := make(map[string]bool)

	for _, log := range logs {
		if len(log.Topics) < 3 {
			logger.Warn("Transfer log has less than 3 topics",
				"topics", log.Topics,
				"tx_hash", log.TransactionHash,
			)
			continue
		}

		// Topics[0] = Transfer signature
		// Topics[1] = from address (indexed)
		// Topics[2] = to address (indexed)
		// Data = amount (not indexed)

		// Extract 'from' (topics[1]) and 'to' (topics[2]) addresses.
		// Topics are 32 bytes, address is last 20 bytes.
		fromAddressTopic := log.Topics[1]
		toAddressTopic := log.Topics[2]
		if len(fromAddressTopic) < 64 || len(toAddressTopic) < 64 { // hex string without 0x prefix should be 64 chars
			continue
		}

		// Get last 40 hex chars (20 bytes) and add 0x prefix.
		fromAddress := "0x" + fromAddressTopic[len(fromAddressTopic)-40:]
		toAddress := "0x" + toAddressTopic[len(toAddressTopic)-40:]

		fromMonitored := e.config.TwoWayIndexing && e.pubkeyStore.Exist(enum.NetworkTypeEVM, evm.ToChecksumAddress(fromAddress))
		toMonitored := e.pubkeyStore.Exist(enum.NetworkTypeEVM, evm.ToChecksumAddress(toAddress))
		if fromMonitored || toMonitored {
			matchedTxHashes[log.TransactionHash] = true
			logger.Info("MATCHED ERC20 TRANSFER",
				"tx_hash", log.TransactionHash,
				"from_address", fromAddress,
				"to_address", toAddress,
			)
		}
	}

	logger.Debug("[ERC20 TRANSFERS]",
		"from_block", fromBlock,
		"to_block", toBlock,
		"total_transfer_events", len(logs),
		"matched_tx_hashes", len(matchedTxHashes),
	)

	return matchedTxHashes, nil
}

func (e *EVMIndexer) extractReceiptTxHashes(blocks map[uint64]*evm.Block, traceActive bool) map[uint64][]string {
	txHashMap := make(map[uint64][]string)
	totalTxs := 0
	nativeTransfers := 0
	safeTransfers := 0
	traceContractCalls := 0

	// appendHash deduplicates tx hashes within a block. When traceActive is on,
	// the contract-call branch may append a hash that the monitored-address branch
	// already appended (e.g., a contract call between two monitored addresses).
	appendHash := func(blockNum uint64, hash string, seen map[string]bool) {
		if !seen[hash] {
			txHashMap[blockNum] = append(txHashMap[blockNum], hash)
			seen[hash] = true
		}
	}

	for blockNum, block := range blocks {
		if block == nil {
			continue
		}
		seen := make(map[string]bool) // dedup within this block
		for _, tx := range block.Transactions {
			totalTxs++
			isSafeExecution := evm.IsSafeExecTransaction(tx.Input)
			if !tx.NeedReceipt() && !isSafeExecution && !traceActive {
				continue
			}

			// If no pubkeyStore, fetch receipts based on mode:
			// - trace active: only contract calls + normal NeedReceipt txs
			// - trace inactive: all NeedReceipt txs (backward compatibility)
			if e.pubkeyStore == nil {
				if traceActive {
					isContractCall := tx.Input != "" && tx.Input != "0x"
					if isContractCall || tx.NeedReceipt() || isSafeExecution {
						appendHash(blockNum, tx.Hash, seen)
					}
				} else {
					appendHash(blockNum, tx.Hash, seen)
				}
				continue
			}

			if !isSafeExecution {
				// OPTIMIZATION: Only fetch receipts for native transfers involving monitored addresses.
				isNativeTransfer := tx.To != "" && (tx.Input == "" || tx.Input == "0x")

				if isNativeTransfer {
					toMonitored := e.pubkeyStore.Exist(enum.NetworkTypeEVM, evm.ToChecksumAddress(tx.To))
					fromMonitored := e.config.TwoWayIndexing && tx.From != "" && e.pubkeyStore.Exist(enum.NetworkTypeEVM, evm.ToChecksumAddress(tx.From))
					if toMonitored || fromMonitored {
						nativeTransfers++
						appendHash(blockNum, tx.Hash, seen)
					}
				}
			} else {
				// Filter must match ExtractSafeTransfers acceptance criteria.
				params, err := evm.DecodeGnosisSafeExecTransaction(tx.Input)
				if err != nil {
					logger.Debug("[DECODE GNOSIS SAFE EXEC TRANSACTION ERROR]", "error", err)
					continue
				}
				if params.Operation != 0 || params.Value.Sign() <= 0 || len(params.Data) != 0 {
					continue
				}

				toMonitored := e.pubkeyStore.Exist(enum.NetworkTypeEVM, evm.ToChecksumAddress(params.To))
				fromMonitored := e.config.TwoWayIndexing && tx.To != "" && e.pubkeyStore.Exist(enum.NetworkTypeEVM, evm.ToChecksumAddress(tx.To))
				if toMonitored || fromMonitored {
					safeTransfers++
					appendHash(blockNum, tx.Hash, seen)
				}
			}

			// When trace mode is active, fetch receipts for ALL contract calls.
			// Internal transfers may involve monitored addresses even when
			// top-level from/to don't (e.g., router/aggregator txs).
			if traceActive && !isSafeExecution {
				isContractCall := tx.Input != "" && tx.Input != "0x"
				if isContractCall {
					traceContractCalls++
					appendHash(blockNum, tx.Hash, seen)
				}
			}
		}
	}

	if e.pubkeyStore != nil {
		logger.Debug("[SELECTIVE RECEIPTS - NATIVE]",
			"total_txs", totalTxs,
			"native_transfers_matched", nativeTransfers,
			"safe_transfers_matched", safeTransfers,
			"trace_contract_calls", traceContractCalls,
		)
	}

	return txHashMap
}

// fetchTraces calls debug_traceTransaction for successful contract calls.
// Uses the dedicated traceFailover pool (not main failover).
func (e *EVMIndexer) fetchTraces(
	ctx context.Context,
	blocks map[uint64]*evm.Block,
	receipts map[string]*evm.TxnReceipt,
) map[string]*evm.CallTrace {
	if e.traceFailover == nil {
		return nil
	}

	// Collect trace candidates: successful contract calls
	var candidates []evm.Txn
	for _, block := range blocks {
		if block == nil {
			continue
		}
		for _, tx := range block.Transactions {
			receipt := receipts[tx.Hash]
			if receipt == nil || !receipt.IsSuccessful() {
				continue
			}
			isContractCall := tx.Input != "" && tx.Input != "0x"
			if !isContractCall {
				continue
			}
			candidates = append(candidates, tx)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	var (
		mu     sync.Mutex
		traces = make(map[string]*evm.CallTrace, len(candidates))
	)

	g, gctx := errgroup.WithContext(ctx)
	concurrency := e.config.TraceThrottle.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	g.SetLimit(concurrency)

	for _, tx := range candidates {
		g.Go(func() error {
			trace, err := e.traceWithProviderAwareness(gctx, tx.Hash)
			if err != nil {
				logger.Warn("debug_traceTransaction failed", "tx", tx.Hash, "error", err)
				return nil // don't fail the whole batch
			}
			mu.Lock()
			traces[tx.Hash] = trace
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return traces
}

// traceWithProviderAwareness iterates trace providers with single-attempt execution.
//
// Why not ExecuteWithRetryProvider: it wraps in retry.Constant(fn, 5s, 3), so a
// misconfigured node burns ~10s per tx before we can detect "method not found".
// Instead, we type-assert provider.Client to evm.EthereumAPI and call
// DebugTraceTransaction directly — one attempt per provider.
//
// On ANY error (capability or transient), we continue to the next provider.
// Only return lastErr after exhausting the provider list.
//
// Providers are shuffled per-call to distribute trace load evenly.
func (e *EVMIndexer) traceWithProviderAwareness(ctx context.Context, txHash string) (*evm.CallTrace, error) {
	providers := e.traceFailover.GetAvailableProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no available trace providers")
	}

	// Shuffle to distribute load across trace nodes
	rand.Shuffle(len(providers), func(i, j int) {
		providers[i], providers[j] = providers[j], providers[i]
	})

	var lastErr error
	for _, provider := range providers {
		client, ok := provider.Client.(evm.EthereumAPI)
		if !ok {
			continue
		}

		start := time.Now()
		trace, err := client.DebugTraceTransaction(ctx, txHash)
		elapsed := time.Since(start)

		if err == nil {
			e.traceFailover.RecordSuccess(provider, elapsed)
			return trace, nil
		}

		lastErr = err
		errMsg := strings.ToLower(err.Error())

		// Capability error: node doesn't support debug_* — blacklist for 24h.
		// Primary: JSON-RPC -32601 (Method Not Found). Fallback: provider-specific strings.
		if strings.Contains(errMsg, "-32601") ||
			strings.Contains(errMsg, "method not found") ||
			strings.Contains(errMsg, "method not available") ||
			strings.Contains(errMsg, "method is not supported") ||
			strings.Contains(errMsg, "unsupported method") ||
			strings.Contains(errMsg, "unknown method") ||
			strings.Contains(errMsg, "does not exist") {
			logger.Error("trace provider does not support debug_traceTransaction, blacklisting",
				"provider", provider.Name, "tx", txHash, "error", err)
			e.traceFailover.HandleCapabilityError(provider, elapsed, 24*time.Hour)
			continue
		}

		// All other errors: use the same analyzeError → blacklist/fail path as executeCore.
		logger.Warn("trace provider returned error, trying next",
			"provider", provider.Name, "tx", txHash, "error", err)
		e.traceFailover.AnalyzeAndHandleError(provider, err, elapsed)
		continue
	}
	return nil, lastErr
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
		providerIdx := i % len(providers)
		provider := providers[providerIdx]

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
					true,
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
	traces map[string]*evm.CallTrace,
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
		txReceipts := make(map[string]*evm.TxnReceipt)
		for _, txHash := range txHashMap[num] {
			if receipt := allReceipts[txHash]; receipt != nil {
				txReceipts[txHash] = receipt
			}
		}

		typesBlock, err := e.convertBlock(block, txReceipts, traces)
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
	// Use the same optimized flow as normal processing
	blocks := make(map[uint64]*evm.Block)

	for _, num := range blockNums {
		var eb *evm.Block
		hexNum := fmt.Sprintf("0x%x", num)

		err := e.failover.ExecuteWithRetry(ctx, func(c evm.EthereumAPI) error {
			var err error
			eb, err = c.GetBlockByNumber(ctx, hexNum, true)
			return err
		})

		if err != nil {
			logger.Warn("fallback individual block fetch failed", "block", num, "error", err)
			continue
		}

		blocks[num] = eb
	}

	// Reuse the optimized processBlocksAndReceipts flow with selective receipt fetching
	return e.processBlocksAndReceipts(ctx, blockNums, blocks, false)
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
	traces map[string]*evm.CallTrace,
) (*types.Block, error) {
	num, _ := utils.ParseHexUint64(eb.Number)
	ts, _ := utils.ParseHexUint64(eb.Timestamp)
	var allTransfers []types.Transaction
	for _, tx := range eb.Transactions {
		receipt := receipts[tx.Hash]
		if receipt == nil {
			continue
		}
		if !receipt.IsSuccessful() {
			logger.Debug("[RECEIPTS] skipping failed tx", "tx", tx.Hash, "status", receipt.Status, "block", num)
			continue
		}
		logger.Info("[RECEIPTS]", "tx", tx.Hash, "receipt", receipt)
		var transfers []types.Transaction
		fee := tx.CalcFee(receipt)
		traced := false

		// Use pre-fetched trace if available
		if traces != nil {
			if trace, ok := traces[tx.Hash]; ok && trace != nil {
				transfers = append(transfers,
					evm.ExtractInternalTransfers(trace, tx, fee, e.GetNetworkId(), num, ts)...)
				traced = true
			}
		}

		// Fallback: Safe heuristic when trace not available
		if !traced && evm.IsSafeExecTransaction(tx.Input) {
			transfers = append(transfers,
				evm.ExtractSafeTransfers(tx, receipt, e.GetNetworkId(), num, ts)...)
		}

		// Always extract top-level transfers (native + ERC20)
		transfers = append(transfers, tx.ExtractTransfers(e.GetNetworkId(), receipt, num, ts)...)

		// Cross-source dedup: trace + Safe + ExtractTransfers may overlap
		transfers = utils.DedupTransfers(transfers)
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
