package bitcoin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

// BitcoinClient implements the BitcoinAPI interface
type BitcoinClient struct {
	*rpc.BaseClient
}

// NewBitcoinClient creates a new Bitcoin RPC client
func NewBitcoinClient(
	url string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rateLimiter *ratelimiter.PooledRateLimiter,
) *BitcoinClient {
	return &BitcoinClient{
		BaseClient: rpc.NewBaseClient(
			url,
			rpc.NetworkBitcoin,
			rpc.ClientTypeRPC,
			auth,
			timeout,
			rateLimiter,
		),
	}
}

// GetBlockCount returns the current block count
func (c *BitcoinClient) GetBlockCount(ctx context.Context) (uint64, error) {
	resp, err := c.CallRPC(ctx, "getblockcount", nil)
	if err != nil {
		return 0, fmt.Errorf("getblockcount failed: %w", err)
	}

	var result uint64
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block count: %w", err)
	}
	return result, nil
}

// GetBlockHash returns the block hash for a given height
func (c *BitcoinClient) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	resp, err := c.CallRPC(ctx, "getblockhash", []any{height})
	if err != nil {
		return "", fmt.Errorf("getblockhash failed: %w", err)
	}

	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal block hash: %w", err)
	}
	return result, nil
}

// GetBlock returns a block by hash with specified verbosity
// Verbosity levels:
// 0: Returns hex-encoded block data
// 1: Returns block with transaction IDs
// 2: Returns block with full transaction details (recommended for indexing)
func (c *BitcoinClient) GetBlock(ctx context.Context, hash string, verbosity int) (*Block, error) {
	resp, err := c.CallRPC(ctx, "getblock", []any{hash, verbosity})
	if err != nil {
		return nil, fmt.Errorf("getblock failed: %w", err)
	}

	var result Block
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &result, nil
}

// GetBlockByHeight returns a block by height
// This is a convenience method that combines GetBlockHash and GetBlock
func (c *BitcoinClient) GetBlockByHeight(ctx context.Context, height uint64, verbosity int) (*Block, error) {
	// First get the block hash
	hash, err := c.GetBlockHash(ctx, height)
	if err != nil {
		return nil, fmt.Errorf("failed to get block hash for height %d: %w", height, err)
	}

	// Then get the full block
	block, err := c.GetBlock(ctx, hash, verbosity)
	if err != nil {
		return nil, fmt.Errorf("failed to get block for hash %s: %w", hash, err)
	}

	// Set the height explicitly (some APIs may not include it)
	block.Height = height

	return block, nil
}

// GetBlockchainInfo returns blockchain information
func (c *BitcoinClient) GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error) {
	resp, err := c.CallRPC(ctx, "getblockchaininfo", nil)
	if err != nil {
		return nil, fmt.Errorf("getblockchaininfo failed: %w", err)
	}

	var result BlockchainInfo
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blockchain info: %w", err)
	}
	return &result, nil
}

// GetRawMempool returns all transaction IDs in the mempool
// If verbose is false, returns []string of txids
// If verbose is true, returns map[string]MempoolEntry with details
func (c *BitcoinClient) GetRawMempool(ctx context.Context, verbose bool) (interface{}, error) {
	resp, err := c.CallRPC(ctx, "getrawmempool", []interface{}{verbose})
	if err != nil {
		return nil, fmt.Errorf("getrawmempool failed: %w", err)
	}

	if !verbose {
		// Parse as string array
		var txids []string
		if err := json.Unmarshal(resp.Result, &txids); err != nil {
			return nil, fmt.Errorf("failed to unmarshal mempool txids: %w", err)
		}
		return txids, nil
	}

	// Parse as map of entries
	var entries map[string]MempoolEntry
	if err := json.Unmarshal(resp.Result, &entries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mempool entries: %w", err)
	}
	return entries, nil
}

// GetRawTransaction returns a transaction by txid
// If verbose is false, returns raw hex string
// If verbose is true, returns Transaction struct
func (c *BitcoinClient) GetRawTransaction(ctx context.Context, txid string, verbose bool) (*Transaction, error) {
	verbosity := 0
	if verbose {
		verbosity = 2 // Verbosity 2 includes prevout data for fee calculation
	}

	resp, err := c.CallRPC(ctx, "getrawtransaction", []interface{}{txid, verbosity})
	if err != nil {
		return nil, fmt.Errorf("getrawtransaction failed for %s: %w", txid, err)
	}

	if !verbose {
		// Raw hex string - not useful for our purposes
		return nil, fmt.Errorf("raw hex transaction not supported, use verbose=true")
	}

	var tx Transaction
	if err := json.Unmarshal(resp.Result, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction %s: %w", txid, err)
	}

	return &tx, nil
}

// GetMempoolEntry returns mempool entry for a specific transaction
func (c *BitcoinClient) GetMempoolEntry(ctx context.Context, txid string) (*MempoolEntry, error) {
	resp, err := c.CallRPC(ctx, "getmempoolentry", []interface{}{txid})
	if err != nil {
		return nil, fmt.Errorf("getmempoolentry failed for %s: %w", txid, err)
	}

	var entry MempoolEntry
	if err := json.Unmarshal(resp.Result, &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mempool entry %s: %w", txid, err)
	}

	return &entry, nil
}
