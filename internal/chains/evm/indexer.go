package evm

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
		client: NewEvmClient(nodes, clientCfg),
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

	result, err := i.client.Call(ctx, "eth_blockNumber", []any{})
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
	result, err := i.client.Call(ctx, "eth_getBlockByNumber", []any{hexNum, true})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	return i.parseBlock(result)
}

func (i *Indexer) GetBlocks(from, to int64) ([]chains.BlockResult, error) {
	var results []chains.BlockResult

	if from > to {
		return results, nil
	}

	ctx := context.Background()
	batch := make([]rpc.Request, 0, to-from+1)
	idToNumber := make(map[int]int64)

	id := 1
	for n := from; n <= to; n++ {
		batch = append(batch, rpc.Request{
			JSONRPC: "2.0",
			Method:  "eth_getBlockByNumber",
			Params:  []any{fmt.Sprintf("0x%x", n), true},
			ID:      id,
		})
		idToNumber[id] = n
		id++
	}

	responses, err := i.client.BatchCall(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("batch call failed: %w", err)
	}

	// Map response by ID
	responseMap := make(map[int]rpc.Response)
	for _, resp := range responses {
		responseMap[resp.ID] = resp
	}

	for _, req := range batch {

		number := idToNumber[req.ID]
		resp, ok := responseMap[req.ID]
		if !ok {
			results = append(results, chains.BlockResult{
				Number: number,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNotFound,
					Message:   "missing response",
				},
			})
			continue
		}

		if resp.Error != nil {
			results = append(results, chains.BlockResult{
				Number: number,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNotFound,
					Message:   resp.Error.Message,
				},
			})
			continue
		}

		if len(resp.Result) == 0 || string(resp.Result) == "null" {
			results = append(results, chains.BlockResult{
				Number: number,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockNil,
					Message:   "block is nil",
				},
			})
			continue
		}

		var block rawBlock
		if err := json.Unmarshal(resp.Result, &block); err != nil {
			results = append(results, chains.BlockResult{
				Number: number,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockUnmarshal,
					Message:   err.Error(),
				},
			})
			continue
		}

		parsedBlock, err := i.parseBlock(resp.Result)
		if err != nil {
			results = append(results, chains.BlockResult{
				Number: number,
				Error: &chains.Error{
					ErrorType: chains.ErrorTypeBlockUnmarshal,
					Message:   err.Error(),
				},
			})
			continue
		}

		results = append(results, chains.BlockResult{
			Number: number,
			Block:  parsedBlock,
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
