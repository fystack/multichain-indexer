package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/fystack/indexer/internal/config"
	"github.com/fystack/indexer/internal/ratelimiter"
	"github.com/fystack/indexer/internal/types"
)

type Indexer struct {
	client *Client
	name   string
	config config.ChainConfig
}

func NewIndexer(nodes []string) *Indexer {
	return NewIndexerWithConfig(nodes, config.ChainConfig{
		RateLimit: config.RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		Client: config.ClientConfig{
			RequestTimeout: 30 * time.Second,
			MaxRetries:     3,
			RetryDelay:     1 * time.Second,
		},
	})
}

func NewIndexerWithConfig(nodes []string, config config.ChainConfig) *Indexer {
	clientConfig := ClientConfig{
		RequestTimeout: config.Client.RequestTimeout,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: config.RateLimit.RequestsPerSecond,
			BurstSize:         config.RateLimit.BurstSize,
		},
		MaxRetries: config.Client.MaxRetries,
		RetryDelay: config.Client.RetryDelay,
	}

	return &Indexer{
		client: NewClientWithConfig(nodes, clientConfig),
		name:   "evm",
		config: config,
	}
}

func (i *Indexer) GetName() string {
	return i.name
}

func (i *Indexer) GetLatestBlockNumber() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), i.config.Client.RequestTimeout)
	defer cancel()

	result, err := i.client.CallWithContext(ctx, "eth_blockNumber", []any{})
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
	result, err := i.client.CallWithContext(ctx, "eth_getBlockByNumber", []any{hexNumber, true})
	if err != nil {
		return nil, err
	}

	return i.parseBlock(result)
}

func (i *Indexer) GetBlocks(from, to int64) ([]*types.Block, error) {
	var blocks []*types.Block
	for blockNum := from; blockNum <= to; blockNum++ {
		block, err := i.GetBlock(blockNum)
		if err != nil {
			return nil, fmt.Errorf("failed to get block %d: %w", blockNum, err)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
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
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid block data: %w", err)
	}

	block := &types.Block{
		Hash:       raw["hash"].(string),
		ParentHash: raw["parentHash"].(string),
	}

	if hexNum, ok := raw["number"].(string); ok {
		if num, err := strconv.ParseInt(hexNum, 0, 64); err == nil {
			block.Number = num
		}
	}

	if hexTime, ok := raw["timestamp"].(string); ok {
		if ts, err := strconv.ParseInt(hexTime, 0, 64); err == nil {
			block.Timestamp = ts
		}
	}

	if txs, ok := raw["transactions"].([]any); ok {
		for idx, tx := range txs {
			if txMap, ok := tx.(map[string]any); ok {
				toStr, _ := txMap["to"].(string)
				transaction := types.Transaction{
					Hash:             txMap["hash"].(string),
					From:             txMap["from"].(string),
					To:               toStr,
					Value:            txMap["value"].(string),
					BlockNumber:      block.Number,
					BlockHash:        block.Hash,
					TransactionIndex: idx,
					Status:           "success", // EVM mặc định
				}
				block.Transactions = append(block.Transactions, transaction)
			}
		}
	}

	return block, nil
}
