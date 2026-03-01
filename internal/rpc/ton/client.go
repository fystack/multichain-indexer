package ton

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// ListTransactions returns transactions for an account.
// Transactions are returned in reverse chronological order (newest first).
// Use lastLT=0 and lastHash=nil for initial fetch from the account's latest transaction.
func (c *Client) ListTransactions(ctx context.Context, addr *address.Address, limit uint32, lastLT uint64, lastHash []byte) ([]*tlb.Transaction, error) {
	if limit == 0 {
		limit = 1
	}

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

	// Page backwards from account tip until we reach the saved cursor.
	var (
		allNewTxs []*tlb.Transaction
		fetchLT   = account.LastTxLT
		fetchHash = account.LastTxHash
	)

	for {
		txs, listErr := c.api.ListTransactions(ctx, addr, limit, fetchLT, fetchHash)
		if listErr != nil {
			if errors.Is(listErr, ton.ErrNoTransactionsWereFound) && len(allNewTxs) > 0 {
				break
			}
			return nil, fmt.Errorf("failed to list transactions: %w", listErr)
		}
		if len(txs) == 0 {
			break
		}

		oldest := txs[0]
		for _, tx := range txs {
			if tx.LT > lastLT {
				allNewTxs = append(allNewTxs, tx)
			}
			if tx.LT < oldest.LT {
				oldest = tx
			}
		}

		// Reached already-processed range.
		if oldest.LT <= lastLT {
			break
		}
		// No more history.
		if oldest.PrevTxLT == 0 || len(oldest.PrevTxHash) == 0 {
			break
		}

		fetchLT = oldest.PrevTxLT
		fetchHash = oldest.PrevTxHash
	}

	// Keep existing contract used by indexer: newest first.
	sort.Slice(allNewTxs, func(i, j int) bool {
		return allNewTxs[i].LT > allNewTxs[j].LT
	})

	return allNewTxs, nil
}

func (c *Client) GetLatestMasterchainSeqno(ctx context.Context) (uint64, error) {
	master, err := c.getMasterchainInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get masterchain info: %w", err)
	}
	return uint64(master.SeqNo), nil
}

func (c *Client) ResolveJettonMasterAddress(ctx context.Context, jettonWallet string) (string, error) {
	walletAddr, err := parseTONAddressAny(jettonWallet)
	if err != nil {
		return "", fmt.Errorf("invalid jetton wallet address: %w", err)
	}

	master, err := c.getMasterchainInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get masterchain info: %w", err)
	}

	res, err := c.api.WaitForBlock(master.SeqNo).RunGetMethod(ctx, master, walletAddr, "get_wallet_data")
	if err != nil {
		return "", fmt.Errorf("run get_wallet_data: %w", err)
	}

	masterSlice, err := res.Slice(2)
	if err != nil {
		return "", fmt.Errorf("parse get_wallet_data master address: %w", err)
	}

	masterAddr, err := masterSlice.LoadAddr()
	if err != nil {
		return "", fmt.Errorf("load master address from stack: %w", err)
	}
	if masterAddr == nil {
		return "", fmt.Errorf("jetton master address is nil")
	}

	return masterAddr.StringRaw(), nil
}

func parseTONAddressAny(addr string) (*address.Address, error) {
	if raw, err := address.ParseRawAddr(addr); err == nil {
		return raw, nil
	}
	return address.ParseAddr(addr)
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = nil
	c.pool = nil
	return nil
}
