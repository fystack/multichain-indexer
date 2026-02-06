package ton

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
)

type Client struct {
	api     ton.APIClientWrapped
	pool    *liteclient.ConnectionPool
	baseURL string
	mu      sync.RWMutex

	// Cache for masterchain info to avoid redundant calls during mass polling
	masterCache     *ton.BlockIDExt
	masterCacheTime time.Time
}

type ClientConfig struct {
	ConfigURL string
}

func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	pool := liteclient.NewConnectionPool()

	if err := pool.AddConnectionsFromConfigUrl(ctx, cfg.ConfigURL); err != nil {
		return nil, fmt.Errorf("failed to fetch/parse global config: %w", err)
	}

	// tonutils-go handles failover between lite servers in the pool
	api := ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetry()

	return &Client{
		api:     api,
		pool:    pool,
		baseURL: cfg.ConfigURL,
	}, nil
}

func NewClientFromPool(pool *liteclient.ConnectionPool) *Client {
	api := ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetry()
	return &Client{
		api:  api,
		pool: pool,
	}
}

// getMasterchainInfo returns the current masterchain block info, using cache if valid.
func (c *Client) getMasterchainInfo(ctx context.Context) (*ton.BlockIDExt, error) {
	c.mu.Lock()
	if c.masterCache != nil && time.Since(c.masterCacheTime) < time.Second {
		defer c.mu.Unlock()
		return c.masterCache, nil
	}
	c.mu.Unlock()

	// Fetch new info
	master, err := c.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.masterCache = master
	c.masterCacheTime = time.Now()
	c.mu.Unlock()

	return master, nil
}

// GetAccountState returns the current state of an account.
func (c *Client) GetAccountState(ctx context.Context, addr *address.Address) (*tlb.Account, error) {
	// Get the current masterchain block for consistency
	master, err := c.getMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get masterchain info: %w", err)
	}

	// Get the account state at the current block
	account, err := c.api.GetAccount(ctx, master, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to get account state: %w", err)
	}

	return account, nil
}

// ListTransactions returns transactions for an account.
// Transactions are returned in reverse chronological order (newest first).
// Use lastLT=0 and lastHash=nil for initial fetch from the account's latest transaction.
func (c *Client) ListTransactions(ctx context.Context, addr *address.Address, limit uint32, lastLT uint64, lastHash []byte) ([]*tlb.Transaction, error) {
	// Get the current masterchain block for consistency
	master, err := c.getMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get masterchain info: %w", err)
	}

	// Always get current account state to check for new transactions
	account, err := c.api.GetAccount(ctx, master, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err)
	}

	// Account not active or doesn't exist
	if !account.IsActive {
		return nil, nil
	}

	// If cursor is at or ahead of latest, no new transactions
	if lastLT >= account.LastTxLT {
		return nil, nil
	}

	// Fetch transactions from the LATEST, going backwards
	txs, err := c.api.ListTransactions(ctx, addr, limit, account.LastTxLT, account.LastTxHash)
	if err != nil {
		return nil, fmt.Errorf("failed to list transactions: %w", err)
	}

	// Filter to only return transactions NEWER than our cursor
	var newTxs []*tlb.Transaction
	for _, tx := range txs {
		if tx.LT > lastLT {
			newTxs = append(newTxs, tx)
		}
	}

	return newTxs, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = nil
	c.pool = nil
	return nil
}
