package cardano

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type CardanoClient struct {
	*rpc.BaseClient
}

// NewCardanoClient creates a new Cardano RPC client
// Uses Blockfrost API (https://blockfrost.io/) or compatible Cardano REST API
func NewCardanoClient(
	baseURL string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter,
) *CardanoClient {
	return &CardanoClient{
		BaseClient: rpc.NewBaseClient(
			baseURL,
			"cardano",
			rpc.ClientTypeREST,
			auth,
			timeout,
			rl,
		),
	}
}

// GetLatestBlockNumber fetches the latest block number from Cardano
func (c *CardanoClient) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	// Using Blockfrost API: GET /blocks/latest
	data, err := c.Do(ctx, "GET", "/blocks/latest", nil, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest block: %w", err)
	}

	var block BlockResponse
	if err := json.Unmarshal(data, &block); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block response: %w", err)
	}

	return block.Height, nil
}

// GetBlockByNumber fetches a block by its height
func (c *CardanoClient) GetBlockByNumber(ctx context.Context, blockNumber uint64) (*Block, error) {
	endpoint := fmt.Sprintf("/blocks/%d", blockNumber)
	data, err := c.Do(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", blockNumber, err)
	}

	var blockResp BlockResponse
	if err := json.Unmarshal(data, &blockResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block response: %w", err)
	}

	// Fetch transactions for this block
	txHashes, err := c.GetTransactionsByBlock(ctx, blockNumber)
	if err != nil {
		logger.Warn("failed to fetch transactions for block", "block", blockNumber, "error", err)
		txHashes = []string{}
	}

	// Convert transactions
	txs := make([]Transaction, 0, len(txHashes))
	for _, txHash := range txHashes {
		tx, err := c.GetTransaction(ctx, txHash)
		if err != nil {
			logger.Warn("failed to fetch transaction", "tx_hash", txHash, "error", err)
			continue
		}
		if tx != nil {
			txs = append(txs, *tx)
		}
	}

	return &Block{
		Hash:       blockResp.Hash,
		Height:     blockResp.Height,
		Slot:       blockResp.Slot,
		Time:       blockResp.Time,
		ParentHash: blockResp.ParentHash,
		Txs:        txs,
	}, nil
}

// GetBlockHash fetches the hash of a block by its height
func (c *CardanoClient) GetBlockHash(ctx context.Context, blockNumber uint64) (string, error) {
	endpoint := fmt.Sprintf("/blocks/%d", blockNumber)
	data, err := c.Do(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get block hash: %w", err)
	}

	var block BlockResponse
	if err := json.Unmarshal(data, &block); err != nil {
		return "", fmt.Errorf("failed to unmarshal block response: %w", err)
	}

	return block.Hash, nil
}

// GetBlockByHash fetches a block by its hash
func (c *CardanoClient) GetBlockByHash(ctx context.Context, blockHash string) (*Block, error) {
	endpoint := fmt.Sprintf("/blocks/%s", blockHash)
	data, err := c.Do(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get block by hash: %w", err)
	}

	var blockResp BlockResponse
	if err := json.Unmarshal(data, &blockResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block response: %w", err)
	}

	// Fetch transactions for this block
	txHashes, err := c.GetTransactionsByBlock(ctx, blockResp.Height)
	if err != nil {
		logger.Warn("failed to fetch transactions for block", "block", blockResp.Height, "error", err)
		txHashes = []string{}
	}

	// Convert transactions
	txs := make([]Transaction, 0, len(txHashes))
	for _, txHash := range txHashes {
		tx, err := c.GetTransaction(ctx, txHash)
		if err != nil {
			logger.Warn("failed to fetch transaction", "tx_hash", txHash, "error", err)
			continue
		}
		if tx != nil {
			txs = append(txs, *tx)
		}
	}

	return &Block{
		Hash:       blockResp.Hash,
		Height:     blockResp.Height,
		Slot:       blockResp.Slot,
		Time:       blockResp.Time,
		ParentHash: blockResp.ParentHash,
		Txs:        txs,
	}, nil
}

// GetTransactionsByBlock fetches all transaction hashes in a block
func (c *CardanoClient) GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]string, error) {
	endpoint := fmt.Sprintf("/blocks/%d/txs", blockNumber)
	data, err := c.Do(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions for block %d: %w", blockNumber, err)
	}

	var txHashes []string
	if err := json.Unmarshal(data, &txHashes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transactions response: %w", err)
	}

	return txHashes, nil
}

// GetTransaction fetches a transaction by its hash
func (c *CardanoClient) GetTransaction(ctx context.Context, txHash string) (*Transaction, error) {
	endpoint := fmt.Sprintf("/txs/%s", txHash)
	data, err := c.Do(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction %s: %w", txHash, err)
	}

	var txResp TransactionResponse
	if err := json.Unmarshal(data, &txResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction response: %w", err)
	}

	// Convert inputs
	inputs := make([]Input, 0, len(txResp.Inputs))
	for _, inp := range txResp.Inputs {
		amount, _ := strconv.ParseUint(inp.Amount, 10, 64)
		inputs = append(inputs, Input{
			Address: inp.Address,
			Amount:  amount,
			TxHash:  inp.TxHash,
			Index:   inp.Index,
		})
	}

	// Convert outputs
	outputs := make([]Output, 0, len(txResp.Outputs))
	for _, out := range txResp.Outputs {
		amount, _ := strconv.ParseUint(out.Amount, 10, 64)
		outputs = append(outputs, Output{
			Address: out.Address,
			Amount:  amount,
			Index:   out.Index,
		})
	}

	fees, _ := strconv.ParseUint(txResp.Fees, 10, 64)

	return &Transaction{
		Hash:     txResp.Hash,
		Slot:     txResp.Block.Slot,
		BlockNum: txResp.Block.Height,
		Inputs:   inputs,
		Outputs:  outputs,
		Fee:      fees,
	}, nil
}

