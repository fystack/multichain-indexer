package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/fystack/indexer/internal/types"
)

type Indexer struct {
	client *Client
	name   string
	config types.ChainConfig
}

func NewIndexer(nodes []string) *Indexer {
	return NewIndexerWithConfig(nodes, types.ChainConfig{
		RateLimit: types.RateLimitConfig{
			RequestsPerSecond: 10,
			BurstSize:         20,
		},
		Client: types.ClientConfig{
			RequestTimeout: 30 * time.Second,
			MaxRetries:     3,
			RetryDelay:     1 * time.Second,
		},
	})
}

func NewIndexerWithConfig(nodes []string, config types.ChainConfig) *Indexer {
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
		name:   "tron",
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

	hexStr, ok := result.(string)
	if !ok {
		return 0, fmt.Errorf("invalid block number format")
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
	fmt.Println("result", result)
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

func (i *Indexer) GetRateLimitStats() map[string]map[string]any {
	return i.client.GetRateLimitStats()
}

func (i *Indexer) Close() {
	i.client.Close()
}

func (i *Indexer) parseBlock(data any) (*types.Block, error) {
	blockData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	var rawBlock map[string]any
	if err := json.Unmarshal(blockData, &rawBlock); err != nil {
		return nil, err
	}

	block := &types.Block{
		Hash:       rawBlock["hash"].(string),
		ParentHash: rawBlock["parentHash"].(string),
	}

	// Parse block number
	if numberHex, ok := rawBlock["number"].(string); ok {
		if number, err := strconv.ParseInt(numberHex, 0, 64); err == nil {
			block.Number = number
		}
	}

	// Parse timestamp
	if timestampHex, ok := rawBlock["timestamp"].(string); ok {
		if timestamp, err := strconv.ParseInt(timestampHex, 0, 64); err == nil {
			block.Timestamp = timestamp
		}
	}

	// Parse transactions
	if txs, ok := rawBlock["transactions"].([]any); ok {
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
					Status:           "success", // Default for now
				}
				block.Transactions = append(block.Transactions, transaction)
			}
		}
	}

	return block, nil
}
