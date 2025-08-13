package indexer

import (
	"context"
	"fmt"
	"idx/internal/common/ratelimiter"
	"idx/internal/core"
	"idx/internal/rpc"
	"log/slog"
	"strconv"
	"time"
)

type TronIndexer struct {
	chainName   string
	config      core.ChainConfig
	failover    *rpc.FailoverManager
	rateLimiter *ratelimiter.PooledRateLimiter
}

func NewTronIndexer(config core.ChainConfig) (*TronIndexer, error) {
	var rl *ratelimiter.PooledRateLimiter
	if config.Client.Throttle.RPS > 0 {
		interval := time.Second / time.Duration(config.Client.Throttle.RPS)
		burst := config.Client.Throttle.Burst
		if burst <= 0 {
			burst = 1
		}
		rl = ratelimiter.NewPooledRateLimiter(interval, burst)
	}

	fm := rpc.NewFailoverManager(rpc.DefaultFailoverConfig())

	for i, nodeURL := range config.Nodes {
		name := fmt.Sprintf("%s-%d", config.Name, i)
		if err := fm.AddTronProvider(name, nodeURL.URL, nil, rl); err != nil {
			return nil, fmt.Errorf("add provider: %w", err)
		}
	}

	return &TronIndexer{
		chainName:   config.Name,
		config:      config,
		failover:    fm,
		rateLimiter: rl,
	}, nil
}

func (t *TronIndexer) GetName() string { return t.chainName }

func (t *TronIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
		n, err := c.GetNowBlock(ctx)
		if err != nil {
			return err
		}
		latest = n
		return nil
	})

	if err != nil {
		return 0, err
	}
	return latest, nil
}

func (t *TronIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*core.Block, error) {
	var block *core.Block
	err := t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
		b, err := c.GetBlockByNumber(ctx, strconv.FormatUint(blockNumber, 10), true)
		if err != nil {
			return err
		}
		cb, err := convertTronBlockToCore(t.GetName(), b)
		if err != nil {
			return err
		}
		block = cb
		return nil
	})
	if err != nil {
		return nil, err
	}
	return block, nil
}

func (t *TronIndexer) GetBlocks(ctx context.Context, from, to uint64) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: to < from")
	}

	results := make([]BlockResult, 0, to-from+1)
	for n := from; n <= to; n++ {
		b, err := t.GetBlock(ctx, n)
		if err != nil {
			results = append(results, BlockResult{
				Number: n,
				Block:  nil,
				Error:  &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
			continue
		}
		results = append(results, BlockResult{Number: n, Block: b, Error: nil})
	}
	return results, nil
}

func (t *TronIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A light check by calling GetBlockNumber
	if _, err := t.GetLatestBlockNumber(ctx); err != nil {
		slog.Warn("Tron health check failed", "name", t.GetName(), "err", err)
		return false
	}
	return true
}

func convertTronBlockToCore(networkName string, tb *rpc.TronBlock) (*core.Block, error) {
	if tb == nil {
		return nil, fmt.Errorf("nil tron block")
	}
	blockNumber := uint64(tb.BlockHeader.RawData.Number)
	timestamp := uint64(tb.BlockHeader.RawData.Timestamp)
	tsx := make([]core.Transaction, 0, len(tb.Transactions))
	for _, tx := range tb.Transactions {
		ct, err := convertTronTxToCore(networkName, &tx)
		if err != nil {
			// Skip malformed tx but continue other txs
			slog.Warn("failed to convert transaction", "hash", tx.TxID, "err", err)
			continue
		}
		ct.BlockNumber = blockNumber
		ct.Timestamp = timestamp
		tsx = append(tsx, ct)
	}

	b := &core.Block{
		Number:       blockNumber,
		Hash:         tb.BlockID,
		ParentHash:   tb.BlockHeader.RawData.ParentHash,
		Timestamp:    timestamp,
		Transactions: tsx,
	}
	return b, nil
}

func convertTronTxToCore(networkName string, tx *rpc.TronTransaction) (core.Transaction, error) {

	return core.Transaction{}, nil
}
