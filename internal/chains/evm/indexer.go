package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fystack/transaction-indexer/internal/chains"
	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/ratelimiter"
	"github.com/fystack/transaction-indexer/internal/types"
)

// Indexer represents an EVM chain indexer
type Indexer struct {
	client    *EvmClient
	name      string
	networkId string
	config    config.ChainConfig
}

// NewIndexerWithConfig creates a new EVM indexer with configuration
func NewIndexerWithConfig(nodes []string, cfg config.ChainConfig) *Indexer {
	clientCfg := EvmClientConfig{
		RequestTimeout: cfg.Client.RequestTimeout,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
			BurstSize:         cfg.RateLimit.BurstSize,
		},
		MaxRetries: cfg.Client.MaxRetries,
		RetryDelay: cfg.Client.RetryDelay,
	}

	return &Indexer{
		client:    NewEvmClient(nodes, clientCfg, nil), // EVM doesn't need URL formatting
		name:      chains.ChainEVM,
		networkId: "1", // Default to Ethereum mainnet
		config:    cfg,
	}
}

// GetName returns the chain name
func (i *Indexer) GetName() string {
	return i.name
}

// GetLatestBlockNumber returns the latest block number
func (i *Indexer) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	return i.client.GetLatestBlockNumber(ctx)
}

// GetBlock returns a single block by number
func (i *Indexer) GetBlock(ctx context.Context, number int64) (*types.Block, error) {
	result, err := i.client.GetBlock(ctx, number)
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	return i.parseBlock(result)
}

// GetBlocks returns multiple blocks in a batch
func (i *Indexer) GetBlocks(ctx context.Context, from, to int64) ([]chains.BlockResult, error) {
	var results []chains.BlockResult

	if from > to {
		return results, nil
	}

	// Use client's batch functionality for better performance
	blockMap, err := i.client.GetBlocksBatch(ctx, from, to)
	if err != nil {
		// Fallback to individual calls if batch fails
		for blockNum := from; blockNum <= to; blockNum++ {
			block, err := i.GetBlock(ctx, blockNum)
			result := chains.BlockResult{Number: blockNum}

			if err != nil {
				result.Error = &chains.Error{
					ErrorType: chains.ErrorTypeBlockNotFound,
					Message:   fmt.Sprintf("failed to get block: %v", err),
				}
			} else if block == nil {
				result.Error = &chains.Error{
					ErrorType: chains.ErrorTypeBlockNil,
					Message:   "block is nil",
				}
			} else {
				result.Block = block
			}
			results = append(results, result)
		}
		return results, nil
	}

	// Process batch results
	for blockNum := from; blockNum <= to; blockNum++ {
		blockData, ok := blockMap[blockNum]
		result := chains.BlockResult{Number: blockNum}

		if !ok {
			result.Error = &chains.Error{
				ErrorType: chains.ErrorTypeBlockNotFound,
				Message:   "missing response",
			}
		} else if len(blockData) == 0 || string(blockData) == "null" {
			result.Error = &chains.Error{
				ErrorType: chains.ErrorTypeBlockNil,
				Message:   "block is nil",
			}
		} else {
			parsedBlock, err := i.parseBlock(blockData)
			if err != nil {
				result.Error = &chains.Error{
					ErrorType: chains.ErrorTypeBlockUnmarshal,
					Message:   err.Error(),
				}
			} else {
				result.Block = parsedBlock
			}
		}
		results = append(results, result)
	}

	return results, nil
}

// IsHealthy checks if the indexer is healthy
func (i *Indexer) IsHealthy() bool {
	_, err := i.GetLatestBlockNumber(context.Background())
	return err == nil
}

// GetRateLimitStats returns rate limiting statistics
func (i *Indexer) GetRateLimitStats() map[string]ratelimiter.Stats {
	return i.client.RateLimiter.GetStats()
}

// Close closes the indexer and releases resources
func (i *Indexer) Close() {
	i.client.Close()
}

// parseBlock parses a raw block response into a types.Block
func (i *Indexer) parseBlock(data json.RawMessage) (*types.Block, error) {
	var raw rawBlock
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid block data: %w", err)
	}

	number, err := types.ParseUint64(raw.Number)
	if err != nil {
		return nil, fmt.Errorf("invalid block number: %w", err)
	}

	timestamp, err := types.ParseUint64(raw.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}

	var txs []types.Transaction
	var highProbabilityERC20Txs []rawTx
	var highProbabilityIndices []int

	// First pass: parse native transfers and identify high-probability ERC-20 transfers
	for idx, rawTx := range raw.Transactions {
		// Parse native transfers immediately
		if nativeTx := parseNativeTransfer(rawTx, number, timestamp, i.networkId); nativeTx != nil {
			txs = append(txs, *nativeTx)
		}

		// Only fetch receipts for high-probability ERC-20 transfers
		if isHighProbabilityERC20Transfer(rawTx) {
			highProbabilityERC20Txs = append(highProbabilityERC20Txs, rawTx)
			highProbabilityIndices = append(highProbabilityIndices, idx)
		}
	}

	// Second pass: fetch receipts only for high-probability ERC-20 transactions
	if len(highProbabilityERC20Txs) > 0 {
		slog.Debug("Found high-probability ERC-20 transactions, fetching receipts...", "count", len(highProbabilityERC20Txs))
		receipts, err := i.client.GetTransactionReceiptsBatch(highProbabilityERC20Txs)
		if err != nil {
			slog.Warn("Failed to get transaction receipts", "error", err)
		} else {
			// Parse ERC-20 transfers from receipts
			for idx, receipt := range receipts {
				if receipt != nil {
					erc20Txs := parseERC20TransfersFromReceipt(raw.Transactions[highProbabilityIndices[idx]], receipt, number, timestamp, i.networkId)
					txs = append(txs, erc20Txs...)
				}
			}
		}
	} else {
		slog.Debug("No high-probability ERC-20 transactions found, skipping receipt fetching")
	}

	return &types.Block{
		Number:       number,
		Hash:         raw.Hash,
		ParentHash:   raw.ParentHash,
		Timestamp:    timestamp,
		Transactions: txs,
	}, nil
}
