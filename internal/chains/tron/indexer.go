package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

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

	// Pre-allocate transactions slice
	transactions := make([]types.Transaction, len(raw.Transactions))

	// Parse transactions in parallel or sequentially
	for idx, tx := range raw.Transactions {
		value, _ := new(big.Int).SetString(strings.TrimPrefix(tx.Value, "0x"), 16)

		transactions[idx] = types.Transaction{
			Hash:             tx.Hash,
			From:             tx.From,
			To:               tx.To,
			Value:            value,
			BlockNumber:      number,
			BlockHash:        raw.Hash,
			TransactionIndex: idx,
			Status:           true, // default true; real status in receipt
		}
	}

	return &types.Block{
		Number:       number,
		Hash:         raw.Hash,
		ParentHash:   raw.ParentHash,
		Timestamp:    timestamp,
		Transactions: transactions,
	}, nil
}
