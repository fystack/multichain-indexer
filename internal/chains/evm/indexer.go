package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
		client: &EvmClient{
			HTTPClient: *rpc.NewHTTPClientWithConfig(nodes, clientCfg),
		},
		name:   chains.ChainEVM,
		config: cfg,
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
		return 0, fmt.Errorf("eth_blockNumber failed: %w", err)
	}

	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, fmt.Errorf("failed to decode block number: %w", err)
	}

	num, err := strconv.ParseInt(hexStr, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid block number: %w", err)
	}

	return num, nil
}

func (i *Indexer) GetBlock(number int64) (*types.Block, error) {
	ctx, cancel := context.WithTimeout(context.Background(), i.config.Client.RequestTimeout)
	defer cancel()

	hexNum := fmt.Sprintf("0x%x", number)
	result, err := i.client.callWithContext(ctx, "eth_getBlockByNumber", []any{hexNum, true})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	return i.parseBlock(result)
}

func (i *Indexer) GetBlocks(from, to int64) ([]chains.BlockResult, error) {
	var results []chains.BlockResult

	for n := from; n <= to; n++ {
		block, err := i.GetBlock(n)
		if err != nil {
			slog.Warn("failed to fetch block", "number", n, "err", err)
			results = append(results, chains.BlockResult{
				Number: n,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNotFound,
					Message:   err.Error(),
				},
			})
			continue
		}
		if block == nil {
			results = append(results, chains.BlockResult{
				Number: n,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNil,
					Message:   "block is nil",
				},
			})
			continue
		}

		results = append(results, chains.BlockResult{
			Number: n,
			Block:  block,
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

func (i *Indexer) parseBlock(data json.RawMessage) (*types.Block, error) {
	var raw rawBlock
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid block data: %w", err)
	}

	num, err := strconv.ParseInt(raw.Number, 0, 64)
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
		Number:     num,
		Timestamp:  timestamp,
	}

	for idx, tx := range raw.Transactions {
		block.Transactions = append(block.Transactions, types.Transaction{
			Hash:             tx.Hash,
			From:             tx.From,
			To:               tx.To,
			Value:            tx.Value,
			BlockNumber:      num,
			BlockHash:        raw.Hash,
			TransactionIndex: idx,
			Status:           "success", // TODO: consider checking tx receipt
		})
	}

	return block, nil
}
