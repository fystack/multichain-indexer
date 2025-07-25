package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/fystack/indexer/internal/chains"
	"github.com/fystack/indexer/internal/config"
	"github.com/fystack/indexer/internal/ratelimiter"
	"github.com/fystack/indexer/internal/rpc"
	"github.com/fystack/indexer/internal/types"
)

type Indexer struct {
	client *TronClient
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
		client: &TronClient{
			HTTPClient: rpc.NewHTTPClientWithConfig(nodes, clientConfig),
		},
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
	return i.parseBlock(result)
}

// TRON doesn't support batch fetching
// So we need to fetch one by one
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

func (i *Indexer) parseBlock(data any) (*types.Block, error) {
	// Marshal from `any` to []byte
	blockBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal block failed: %w", err)
	}

	// Unmarshal to struct type-safe
	var raw rawBlock
	if err := json.Unmarshal(blockBytes, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal block failed: %w", err)
	}

	// Parse block number
	number, err := strconv.ParseInt(raw.Number, 0, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid block number: %w", err)
	}

	// Parse timestamp
	timestamp, err := strconv.ParseInt(raw.Timestamp, 0, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}

	// Create block result
	block := &types.Block{
		Hash:       raw.Hash,
		ParentHash: raw.ParentHash,
		Number:     number,
		Timestamp:  timestamp,
	}

	// Assign transactions
	block.Transactions = make([]types.Transaction, 0, len(raw.Transactions))
	for i, tx := range raw.Transactions {
		block.Transactions = append(block.Transactions, types.Transaction{
			Hash:             tx.Hash,
			From:             tx.From,
			To:               tx.To,
			Value:            tx.Value,
			BlockNumber:      number,
			BlockHash:        raw.Hash,
			TransactionIndex: i,
			Status:           "success", // default
		})
	}

	return block, nil
}
