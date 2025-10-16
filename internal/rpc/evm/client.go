package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type Client struct {
	*rpc.BaseClient
}

func NewEthereumClient(
	url string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rateLimiter *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		BaseClient: rpc.NewBaseClient(
			url,
			rpc.NetworkEVM,
			rpc.ClientTypeRPC,
			auth,
			timeout,
			rateLimiter,
		),
	}
}

// GetBlockNumber returns the current block number
func (c *Client) GetBlockNumber(ctx context.Context) (uint64, error) {
	resp, err := c.CallRPC(ctx, "eth_blockNumber", nil)
	if err != nil {
		return 0, fmt.Errorf("eth_blockNumber failed: %w", err)
	}

	var blockHex string
	if err := json.Unmarshal(resp.Result, &blockHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block number: %w", err)
	}

	blockHex = strings.TrimPrefix(blockHex, "0x")
	blockNum, err := strconv.ParseUint(blockHex, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse block number: %w", err)
	}

	return blockNum, nil
}

// GetBlockByNumber returns a block with full transaction data
func (c *Client) GetBlockByNumber(
	ctx context.Context,
	blockNumber string,
	detail bool,
) (*Block, error) {
	if blockNumber == "" {
		blockNumber = "latest"
	}

	resp, err := c.CallRPC(ctx, "eth_getBlockByNumber", []any{blockNumber, detail})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	var block Block
	if err := json.Unmarshal(resp.Result, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &block, nil
}

// BatchGetBlocksByNumber gets multiple blocks efficiently
func (c *Client) BatchGetBlocksByNumber(
	ctx context.Context,
	blockNumbers []uint64,
	fullTxs bool,
) (map[uint64]*Block, error) {
	results := make(map[uint64]*Block)
	if len(blockNumbers) == 0 {
		return results, nil
	}

	ids := c.NextRequestIDs(len(blockNumbers))
	requests := make([]*rpc.RPCRequest, 0, len(blockNumbers))
	idToBlockNum := make(map[int64]uint64, len(blockNumbers))

	for i, n := range blockNumbers {
		hexNum := fmt.Sprintf("0x%x", n)
		id := ids[i]
		requests = append(requests, &rpc.RPCRequest{
			ID:      id,
			JSONRPC: "2.0",
			Method:  "eth_getBlockByNumber",
			Params:  []any{hexNum, fullTxs},
		})
		idToBlockNum[id] = n
	}

	rpcResponses, err := c.DoBatch(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("failed to post batch request: %w", err)
	}

	for _, r := range rpcResponses {
		if r.Error != nil {
			logger.Error("batch get blocks failed", "error", r.Error)
			continue
		}
		if len(r.Result) == 0 || string(r.Result) == "null" {
			continue
		}

		id, ok := r.IDInt64()
		if !ok {
			continue
		}
		blockNum, ok := idToBlockNum[id]
		if !ok {
			continue
		}

		var block Block
		if err := json.Unmarshal(r.Result, &block); err != nil {
			return nil, fmt.Errorf("failed to unmarshal block: %w", err)
		}
		results[blockNum] = &block
	}

	return results, nil
}

// BatchGetTransactionReceipts gets multiple transaction receipts for gas fee calculation
func (c *Client) BatchGetTransactionReceipts(
	ctx context.Context,
	txHashes []string,
) (map[string]*TxnReceipt, error) {
	results := make(map[string]*TxnReceipt)
	if len(txHashes) == 0 {
		return results, nil
	}

	// Generate unique IDs
	ids := c.NextRequestIDs(len(txHashes))
	requests := make([]*rpc.RPCRequest, 0, len(txHashes))
	idToHash := make(map[int64]string, len(txHashes))

	for i, h := range txHashes {
		id := ids[i]
		requests = append(requests, &rpc.RPCRequest{
			ID:      id,
			JSONRPC: "2.0",
			Method:  "eth_getTransactionReceipt",
			Params:  []any{h},
		})
		idToHash[id] = h
	}

	// Send batch request
	rpcResponses, err := c.DoBatch(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("failed to post batch request: %w", err)
	}

	for _, r := range rpcResponses {
		if r.Error != nil {
			slog.Error("batch get transaction receipts failed", "error", r.Error, "provider_url", c.GetURL())
			continue
		}

		id, ok := r.IDInt64()
		if !ok {
			continue
		}
		hash, ok := idToHash[id]
		if !ok {
			continue
		}

		if len(r.Result) == 0 || string(r.Result) == "null" {
			continue
		}

		var receipt TxnReceipt
		if err := json.Unmarshal(r.Result, &receipt); err != nil {
			return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
		}
		results[hash] = &receipt
	}

	return results, nil
}
