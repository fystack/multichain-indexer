package ton

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	tonlib "github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

const (
	masterchainWorkchain   int32 = -1
	masterchainShard       int64 = math.MinInt64
	defaultTxBatchCount          = 100
	getBlockDataMaxRetry         = 3
	getBlockDataRetryDelay       = 200 * time.Millisecond
)

type ClientConfig struct {
	ConfigURL string
}

type Client struct {
	api  tonlib.APIClientWrapped
	pool *liteclient.ConnectionPool
}

func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	configURL := strings.TrimSpace(cfg.ConfigURL)
	if configURL == "" {
		return nil, fmt.Errorf("config URL is required")
	}

	globalCfg, err := liteclient.GetConfigFromUrl(ctx, configURL)
	if err != nil {
		return nil, fmt.Errorf("get global config: %w", err)
	}

	pool := liteclient.NewConnectionPool()
	if err := pool.AddConnectionsFromConfig(ctx, globalCfg); err != nil {
		return nil, fmt.Errorf("add lite connections: %w", err)
	}

	api := tonlib.NewAPIClient(pool, tonlib.ProofCheckPolicyFast).WithRetry()
	api.SetTrustedBlockFromConfig(globalCfg)

	return &Client{
		api:  api,
		pool: pool,
	}, nil
}

func (c *Client) StickyContext(ctx context.Context) context.Context {
	if c == nil || c.api == nil {
		return ctx
	}
	return c.api.Client().StickyContext(ctx)
}

func (c *Client) GetLatestMasterchainInfo(ctx context.Context) (*tonlib.BlockIDExt, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}
	return c.api.GetMasterchainInfo(ctx)
}

func (c *Client) LookupMasterchainBlock(ctx context.Context, seqno uint32) (*tonlib.BlockIDExt, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}

	// WaitForBlock avoids lookup races when asked for a freshly produced seqno.
	return c.api.WaitForBlock(seqno).LookupBlock(
		ctx,
		masterchainWorkchain,
		masterchainShard,
		seqno,
	)
}

func (c *Client) LookupBlock(
	ctx context.Context,
	workchain int32,
	shard int64,
	seqno uint32,
) (*tonlib.BlockIDExt, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}

	// WaitForBlock avoids lookup races when asked for a freshly produced seqno.
	return c.api.WaitForBlock(seqno).LookupBlock(ctx, workchain, shard, seqno)
}

func (c *Client) GetBlockData(ctx context.Context, block *tonlib.BlockIDExt) (*tlb.Block, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}
	if block == nil {
		return nil, fmt.Errorf("ton block is nil")
	}

	var lastErr error
	for attempt := 1; attempt <= getBlockDataMaxRetry; attempt++ {
		var (
			data *tlb.Block
			err  error
		)

		if block.Workchain == masterchainWorkchain {
			// For masterchain blocks, seqno is master seqno.
			data, err = c.api.WaitForBlock(block.SeqNo).GetBlockData(ctx, block)
		} else {
			// Shard seqno is not comparable with master seqno. Using WaitForBlock(shardSeqno)
			// can trigger liteserver error 651 ("too big masterchain block seqno").
			data, err = c.api.GetBlockData(ctx, block)
		}
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !isRetryableResolveBlockError(err) || attempt == getBlockDataMaxRetry {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(getBlockDataRetryDelay):
		}
	}

	return nil, lastErr
}

func (c *Client) GetBlockShardsInfo(
	ctx context.Context,
	master *tonlib.BlockIDExt,
) ([]*tonlib.BlockIDExt, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}
	return c.api.GetBlockShardsInfo(ctx, master)
}

func (c *Client) GetBlockTransactionsV2(
	ctx context.Context,
	masterSeqno uint32,
	block *tonlib.BlockIDExt,
	count uint32,
	after *tonlib.TransactionID3,
) ([]tonlib.TransactionShortInfo, bool, error) {
	if c == nil || c.api == nil {
		return nil, false, fmt.Errorf("ton client not initialized")
	}

	if count == 0 {
		count = defaultTxBatchCount
	}

	if after == nil {
		return c.api.WaitForBlock(masterSeqno).GetBlockTransactionsV2(ctx, block, count)
	}

	return c.api.WaitForBlock(masterSeqno).GetBlockTransactionsV2(ctx, block, count, after)
}

func (c *Client) GetTransaction(
	ctx context.Context,
	block *tonlib.BlockIDExt,
	addr *address.Address,
	lt uint64,
) (*tlb.Transaction, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}
	return c.api.GetTransaction(ctx, block, addr, lt)
}

func (c *Client) GetAccount(ctx context.Context, block *tonlib.BlockIDExt, addr *address.Address) (*tlb.Account, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}
	if block == nil {
		return nil, fmt.Errorf("ton block is nil")
	}
	return c.api.WaitForBlock(block.SeqNo).GetAccount(ctx, block, addr)
}

func (c *Client) ListTransactions(ctx context.Context, addr *address.Address, num uint32, lt uint64, txHash []byte) ([]*tlb.Transaction, error) {
	if c == nil || c.api == nil {
		return nil, fmt.Errorf("ton client not initialized")
	}
	return c.api.ListTransactions(ctx, addr, num, lt, txHash)
}

func (c *Client) ResolveJettonWalletAddress(
	ctx context.Context,
	jettonMaster string,
	ownerAddress string,
) (wallet string, err error) {
	if c == nil || c.api == nil {
		return "", fmt.Errorf("ton client not initialized")
	}

	masterAddr, err := parseTONAddressAny(jettonMaster)
	if err != nil {
		return "", fmt.Errorf("invalid jetton master address: %w", err)
	}
	ownerAddr, err := parseTONAddressAny(ownerAddress)
	if err != nil {
		return "", fmt.Errorf("invalid owner address: %w", err)
	}

	masterBlock, err := c.GetLatestMasterchainInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("get latest masterchain info: %w", err)
	}

	res, err := c.api.WaitForBlock(masterBlock.SeqNo).RunGetMethod(
		ctx,
		masterBlock,
		masterAddr,
		"get_wallet_address",
		cell.BeginCell().MustStoreAddr(ownerAddr).EndCell().BeginParse(),
	)
	if err != nil {
		return "", fmt.Errorf("run get_wallet_address: %w", err)
	}

	walletSlice, err := res.Slice(0)
	if err != nil {
		return "", fmt.Errorf("parse get_wallet_address result: %w", err)
	}

	walletAddr, err := walletSlice.LoadAddr()
	if err != nil {
		return "", fmt.Errorf("load jetton wallet address: %w", err)
	}
	if walletAddr == nil {
		return "", fmt.Errorf("jetton wallet address is nil")
	}

	return walletAddr.StringRaw(), nil
}

func (c *Client) ResolveJettonMasterAddress(
	ctx context.Context,
	jettonWallet string,
) (string, error) {
	_, master, err := c.ResolveJettonWalletData(ctx, jettonWallet)
	if err != nil {
		return "", err
	}
	if master == "" {
		return "", fmt.Errorf("jetton master address is nil")
	}
	return master, nil
}

func (c *Client) ResolveJettonWalletData(
	ctx context.Context,
	jettonWallet string,
) (string, string, error) {
	if c == nil || c.api == nil {
		return "", "", fmt.Errorf("ton client not initialized")
	}

	walletAddr, err := parseTONAddressAny(jettonWallet)
	if err != nil {
		return "", "", fmt.Errorf("invalid jetton wallet address: %w", err)
	}

	masterBlock, err := c.GetLatestMasterchainInfo(ctx)
	if err != nil {
		return "", "", fmt.Errorf("get latest masterchain info: %w", err)
	}

	res, err := c.api.WaitForBlock(masterBlock.SeqNo).RunGetMethod(
		ctx,
		masterBlock,
		walletAddr,
		"get_wallet_data",
	)
	if err != nil {
		return "", "", fmt.Errorf("run get_wallet_data: %w", err)
	}

	ownerSlice, err := res.Slice(1)
	if err != nil {
		return "", "", fmt.Errorf("parse get_wallet_data owner result: %w", err)
	}

	ownerAddr, err := ownerSlice.LoadAddr()
	if err != nil {
		return "", "", fmt.Errorf("load jetton owner address: %w", err)
	}

	masterSlice, err := res.Slice(2)
	if err != nil {
		return "", "", fmt.Errorf("parse get_wallet_data master result: %w", err)
	}

	masterAddr, err := masterSlice.LoadAddr()
	if err != nil {
		return "", "", fmt.Errorf("load jetton master address: %w", err)
	}

	owner := ""
	master := ""
	if ownerAddr != nil {
		owner = ownerAddr.StringRaw()
	}
	if masterAddr != nil {
		master = masterAddr.StringRaw()
	}

	return owner, master, nil
}

func parseTONAddressAny(addr string) (*address.Address, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}
	if parsed, err := address.ParseRawAddr(addr); err == nil {
		return parsed, nil
	}
	return address.ParseAddr(addr)
}

func isRetryableResolveBlockError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "failed to resolve block") ||
		strings.Contains(msg, "lite server error, code 500")
}

func (c *Client) Close() error {
	if c == nil || c.pool == nil {
		return nil
	}
	c.pool.Stop()
	return nil
}
