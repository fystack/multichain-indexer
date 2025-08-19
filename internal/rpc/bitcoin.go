package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/internal/common/ratelimiter"
)

// Bitcoin specific types and methods
type BitcoinClient struct {
	*genericClient
}

// NewBitcoinClient creates a new Bitcoin client (JSON-RPC)
func NewBitcoinClient(url string, auth *AuthConfig, timeout time.Duration, rateLimiter *ratelimiter.PooledRateLimiter) *BitcoinClient {
	return &BitcoinClient{
		genericClient: NewGenericClient(url, NetworkBitcoin, ClientTypeRPC, auth, timeout, rateLimiter),
	}
}

// GetBlockCount returns the current block height
func (b *BitcoinClient) GetBlockCount(ctx context.Context) (int64, error) {
	resp, err := b.CallRPC(ctx, "getblockcount", nil)
	if err != nil {
		return 0, fmt.Errorf("getblockcount failed: %w", err)
	}
	var n int64
	if err := json.Unmarshal(resp.Result, &n); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block count: %w", err)
	}
	return n, nil
}

// GetBestBlockHash returns the hash of the best block
func (b *BitcoinClient) GetBestBlockHash(ctx context.Context) (string, error) {
	resp, err := b.CallRPC(ctx, "getbestblockhash", nil)
	if err != nil {
		return "", fmt.Errorf("getbestblockhash failed: %w", err)
	}
	var h string
	if err := json.Unmarshal(resp.Result, &h); err != nil {
		return "", fmt.Errorf("failed to unmarshal best block hash: %w", err)
	}
	return h, nil
}

// GetBlockHash returns the block hash for a given height
func (b *BitcoinClient) GetBlockHash(ctx context.Context, height int64) (string, error) {
	resp, err := b.CallRPC(ctx, "getblockhash", []any{height})
	if err != nil {
		return "", fmt.Errorf("getblockhash failed: %w", err)
	}
	var h string
	if err := json.Unmarshal(resp.Result, &h); err != nil {
		return "", fmt.Errorf("failed to unmarshal block hash: %w", err)
	}
	return h, nil
}

// BitcoinBlock represents a minimal Bitcoin block view
type BitcoinBlock struct {
	Hash              string   `json:"hash"`
	Height            int64    `json:"height"`
	PreviousBlockHash string   `json:"previousblockhash"`
	Tx                []string `json:"tx"`
}

// GetBlock returns a block by hash
func (b *BitcoinClient) GetBlock(ctx context.Context, hash string) (*BitcoinBlock, error) {
	resp, err := b.CallRPC(ctx, "getblock", []any{hash})
	if err != nil {
		return nil, fmt.Errorf("getblock failed: %w", err)
	}
	var block BitcoinBlock
	if err := json.Unmarshal(resp.Result, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &block, nil
}

// GetRawTransaction returns a transaction hex by txid
func (b *BitcoinClient) GetRawTransaction(ctx context.Context, txid string) (string, error) {
	resp, err := b.CallRPC(ctx, "getrawtransaction", []any{txid})
	if err != nil {
		return "", fmt.Errorf("getrawtransaction failed: %w", err)
	}
	var hex string
	if err := json.Unmarshal(resp.Result, &hex); err != nil {
		return "", fmt.Errorf("failed to unmarshal raw transaction: %w", err)
	}
	return hex, nil
}

// SendRawTransaction broadcasts a signed transaction hex
func (b *BitcoinClient) SendRawTransaction(ctx context.Context, hex string) (string, error) {
	resp, err := b.CallRPC(ctx, "sendrawtransaction", []any{hex})
	if err != nil {
		return "", fmt.Errorf("sendrawtransaction failed: %w", err)
	}
	var txid string
	if err := json.Unmarshal(resp.Result, &txid); err != nil {
		return "", fmt.Errorf("failed to unmarshal txid: %w", err)
	}
	return txid, nil
}

// FailoverManager helpers for Bitcoin
// AddBitcoinProvider adds a Bitcoin provider to the failover manager
func (fm *FailoverManager) AddBitcoinProvider(name, url string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	return fm.AddProvider(name, url, NetworkBitcoin, ClientTypeRPC, auth, rateLimiter)
}

// GetBitcoinClient returns the current provider as a Bitcoin client
func (fm *FailoverManager) GetBitcoinClient() (*BitcoinClient, error) {
	provider, err := fm.GetBestProvider()
	if err != nil {
		return nil, err
	}
	if provider.Network != NetworkBitcoin {
		return nil, fmt.Errorf("current provider is not a Bitcoin network")
	}
	genericClient := provider.Client.(*genericClient)
	return &BitcoinClient{genericClient: genericClient}, nil
}

// ExecuteBitcoinCall executes a function with a Bitcoin client and automatic failover
func (fm *FailoverManager) ExecuteBitcoinCall(ctx context.Context, fn func(*BitcoinClient) error) error {
	return fm.ExecuteWithRetry(ctx, func(client NetworkClient) error {
		if client.GetNetworkType() != NetworkBitcoin {
			return fmt.Errorf("expected Bitcoin client, got %s", client.GetNetworkType())
		}
		btcClient := &BitcoinClient{genericClient: client.(*genericClient)}
		return fn(btcClient)
	})
}
