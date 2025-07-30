package evm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/fystack/indexer/internal/chains"
	"github.com/fystack/indexer/internal/config"
	"github.com/fystack/indexer/internal/ratelimiter"
	"github.com/fystack/indexer/internal/rpc"
	"github.com/fystack/indexer/internal/types"
)

type Indexer struct {
	client *EvmClient
	name   string
	config config.ChainConfig
}

func NewIndexer(nodes []string) *Indexer {
	return NewIndexerWithConfig(nodes, chains.DefaultChainConfig)
}

func NewIndexerWithConfig(nodes []string, config config.ChainConfig) *Indexer {
	clientConfig := rpc.ClientConfig{
		RequestTimeout: config.Client.RequestTimeout,
		RateLimit: rpc.RateLimitConfig{
			RequestsPerSecond: config.RateLimit.RequestsPerSecond,
			BurstSize:         config.RateLimit.BurstSize,
		},
		MaxRetries: config.Client.MaxRetries,
		RetryDelay: config.Client.RetryDelay,
	}

	return &Indexer{
		client: &EvmClient{
			HTTPClient: *rpc.NewHTTPClientWithConfig(nodes, clientConfig),
		},
		name:   chains.ChainEVM,
		config: config,
	}
}

func (i *Indexer) GetName() string {
	return i.name
}

func (i *Indexer) GetLatestBlockNumber() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), i.config.Client.RequestTimeout)
	defer cancel()

	result, err := i.client.callWithContext(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}

	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, fmt.Errorf("failed to decode block number: %w", err)
	}

	return strconv.ParseInt(hexStr, 0, 64)
}

func (i *Indexer) GetBlock(number int64) (*types.Block, error) {
	ctx, cancel := context.WithTimeout(context.Background(), i.config.Client.RequestTimeout)
	defer cancel()

	hexNumber := fmt.Sprintf("0x%x", number)
	result, err := i.client.callWithContext(ctx, "eth_getBlockByNumber", []any{hexNumber, true})
	if err != nil {
		return nil, err
	}

	return i.parseBlock(result)
}

// TODO: Implement batch fetching
// Might be implemented in the future
func (i *Indexer) GetBlocks(from, to int64) ([]chains.BlockResult, error) {
	var results []chains.BlockResult

	for blockNum := from; blockNum <= to; blockNum++ {
		block, err := i.GetBlock(blockNum)
		if err != nil {
			results = append(results, chains.BlockResult{
				Number: blockNum,
				Block:  nil,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNotFound,
					Message:   fmt.Sprintf("failed to get block: %v", err),
				},
			})
			continue
		}

		if block == nil {
			results = append(results, chains.BlockResult{
				Number: blockNum,
				Block:  nil,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNil,
					Message:   "block is nil",
				},
			})
			continue
		}

		results = append(results, chains.BlockResult{
			Number: blockNum,
			Block:  block,
			Error:  nil,
		})
	}

	return results, nil
}

func (i *Indexer) GetBlocksParallel(from, to int64, numWorkers int) ([]chains.BlockResult, error) {
	return nil, errors.New("not implemented")
}

func (i *Indexer) IsHealthy() bool {
	_, err := i.GetLatestBlockNumber()
	return err == nil
}

func (i *Indexer) GetRateLimitStats() map[string]ratelimiter.Stats {
	return i.client.GetRateLimitStats()
}

func (i *Indexer) Close() {
	i.client.Close()
}

func (i *Indexer) parseBlock(data json.RawMessage) (*types.Block, error) {
	var raw rawBlock
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid block data: %w", err)
	}

	number, err := strconv.ParseInt(raw.Number, 0, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid block number: %w", err)
	}

	timestamp, err := strconv.ParseInt(raw.Timestamp, 0, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}

	block := &types.Block{
		Hash:       raw.Hash,
		ParentHash: raw.ParentHash,
		Number:     number,
		Timestamp:  timestamp,
	}

	for idx, tx := range raw.Transactions {
		transaction := types.Transaction{
			Hash:             tx.Hash,
			From:             tx.From,
			To:               tx.To,
			Value:            tx.Value,
			BlockNumber:      number,
			BlockHash:        raw.Hash,
			TransactionIndex: idx,
			Status:           "success",
		}
		block.Transactions = append(block.Transactions, transaction)
	}

	return block, nil
}
