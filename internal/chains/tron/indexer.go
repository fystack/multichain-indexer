package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

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

func NewIndexerWithConfig(nodes []string, cfg config.ChainConfig) *Indexer {
	clientCfg := rpc.ClientConfig{
		RequestTimeout: cfg.Client.RequestTimeout,
		RateLimit: rpc.RateLimitConfig{
			RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
			BurstSize:         cfg.RateLimit.BurstSize,
		},
		MaxRetries: cfg.Client.MaxRetries,
		RetryDelay: cfg.Client.RetryDelay,
	}

	return &Indexer{
		client: NewTronClient(nodes, clientCfg),
		name:   chains.ChainTron,
		config: cfg,
	}
}

func (i *Indexer) GetName() string {
	return i.name
}

func (i *Indexer) GetLatestBlockNumber() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), i.config.Client.RequestTimeout)
	defer cancel()

	result, err := i.client.Call(ctx, "eth_blockNumber", nil)
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
	result, err := i.client.Call(ctx, "eth_getBlockByNumber", []any{hexNumber, true})
	if err != nil {
		return nil, err
	}
	return i.parseBlock(result)
}

func (i *Indexer) GetBlocks(from, to int64) ([]chains.BlockResult, error) {
	var results []chains.BlockResult

	for blockNum := from; blockNum <= to; blockNum++ {
		block, err := i.GetBlock(blockNum)
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
	blockBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal block failed: %w", err)
	}

	var raw rawBlock
	if err := json.Unmarshal(blockBytes, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal block failed: %w", err)
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
		Hash:         raw.Hash,
		ParentHash:   raw.ParentHash,
		Number:       number,
		Timestamp:    timestamp,
		Transactions: make([]types.Transaction, 0, len(raw.Transactions)),
	}

	for i, tx := range raw.Transactions {
		block.Transactions = append(block.Transactions, types.Transaction{
			Hash:             tx.Hash,
			From:             tx.From,
			To:               tx.To,
			Value:            tx.Value,
			BlockNumber:      number,
			BlockHash:        raw.Hash,
			TransactionIndex: i,
			Status:           "success",
		})
	}
	return block, nil
}
