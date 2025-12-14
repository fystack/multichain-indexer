package cardano

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
	"golang.org/x/sync/errgroup"
)

const DefaultTxFetchConcurrency = 4

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

// GetBlockHeaderByNumber fetches only block header by height
func (c *CardanoClient) GetBlockHeaderByNumber(ctx context.Context, blockNumber uint64) (*BlockResponse, error) {
	endpoint := fmt.Sprintf("/blocks/%d", blockNumber)
	data, err := c.Do(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get block header %d: %w", blockNumber, err)
	}
	var br BlockResponse
	if err := json.Unmarshal(data, &br); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block header: %w", err)
	}
	return &br, nil
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
	br, err := c.GetBlockHeaderByNumber(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", blockNumber, err)
	}

	// Fetch transactions for this block
	txHashes, err := c.GetTransactionsByBlock(ctx, blockNumber)
	if err != nil {
		logger.Warn("failed to fetch transactions for block", "block", blockNumber, "error", err)
		txHashes = []string{}
	}

	// Fetch transaction details (parallel-safe)
	txs, _ := c.FetchTransactionsParallel(ctx, txHashes, DefaultTxFetchConcurrency)

	return &Block{
		Hash:       br.Hash,
		Height:     br.Height,
		Slot:       br.Slot,
		Time:       br.Time,
		ParentHash: br.ParentHash,
		Txs:        txs,
	}, nil
}

// GetBlockHash fetches the hash of a block by its height
func (c *CardanoClient) GetBlockHash(ctx context.Context, blockNumber uint64) (string, error) {
	br, err := c.GetBlockHeaderByNumber(ctx, blockNumber)
	if err != nil {
		return "", fmt.Errorf("failed to get block hash: %w", err)
	}
	return br.Hash, nil
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
	// Resolve block hash from height then request txs by hash
	hash, err := c.GetBlockHash(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve block hash: %w", err)
	}
	endpoint := fmt.Sprintf("/blocks/%s/txs", hash)
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

	// Fetch UTXOs (inputs/outputs)
	utxoEndpoint := fmt.Sprintf("/txs/%s/utxos", txHash)
	utxoData, err := c.Do(ctx, "GET", utxoEndpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction utxos %s: %w", txHash, err)
	}

	var utxos TxUTxOsResponse
	if err := json.Unmarshal(utxoData, &utxos); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tx utxos: %w", err)
	}

	// Convert inputs (multi-asset)
	inputs := make([]Input, 0, len(utxos.Inputs))
	for _, inp := range utxos.Inputs {
		inputs = append(inputs, Input{
			Address:    inp.Address,
			Amounts:    inp.Amount,
			TxHash:     inp.TxHash,
			Index:      inp.OutputIndex,
			Collateral: inp.Collateral,
			Reference:  inp.Reference,
		})
	}

	// Convert outputs (multi-asset)
	outputs := make([]Output, 0, len(utxos.Outputs))
	for _, out := range utxos.Outputs {
		outputs = append(outputs, Output{
			Address:    out.Address,
			Amounts:    out.Amount,
			Index:      out.OutputIndex,
			Collateral: out.Collateral,
		})
	}

	fees, _ := strconv.ParseUint(txResp.Fees, 10, 64)

	return &Transaction{
		Hash:          txResp.Hash,
		Slot:          txResp.Slot,
		BlockNum:      txResp.Height,
		Inputs:        inputs,
		Outputs:       outputs,
		Fee:           fees,
		ValidContract: txResp.ValidContract,
	}, nil
}

// FetchTransactionsParallel fetches transactions concurrently with bounded concurrency
func (c *CardanoClient) FetchTransactionsParallel(
	ctx context.Context,
	txHashes []string,
	concurrency int,
) ([]Transaction, error) {
	if concurrency <= 0 {
		concurrency = DefaultTxFetchConcurrency
	}
	if len(txHashes) == 0 {
		return nil, nil
	}

	var (
		mu       sync.Mutex
		results  = make([]Transaction, 0, len(txHashes))
		g, gctx  = errgroup.WithContext(ctx)
		sem      = make(chan struct{}, concurrency)
	)

	for _, h := range txHashes {
		h := h
		sem <- struct{}{}
		g.Go(func() error {
			defer func() { <-sem }()
			tx, err := c.GetTransaction(gctx, h)
			if err != nil {
				logger.Warn("parallel tx fetch failed", "tx_hash", h, "error", err)
				return nil // continue other txs
			}
			if tx != nil {
				mu.Lock()
				results = append(results, *tx)
				mu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		logger.Warn("fetch transactions parallel completed with error", "error", err)
	}
	return results, nil
}

