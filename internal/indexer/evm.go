package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"idx/internal/common/ratelimiter"
	"idx/internal/core"
	"idx/internal/rpc"

	"github.com/shopspring/decimal"
)

// EVMIndexer implements Indexer for EVM-compatible chains
type EVMIndexer struct {
	config      core.ChainConfig
	failover    *rpc.FailoverManager
	rateLimiter *ratelimiter.PooledRateLimiter
}

func NewEVMIndexer(config core.ChainConfig) (*EVMIndexer, error) {
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
		name := fmt.Sprintf("evm-%s-%d", "evm", i)
		if err := fm.AddEthereumProvider(name, nodeURL.URL, nil, rl); err != nil {
			return nil, fmt.Errorf("add provider: %w", err)
		}
	}

	return &EVMIndexer{
		config:      config,
		failover:    fm,
		rateLimiter: rl,
	}, nil
}

func (e *EVMIndexer) GetName() string { return "evm" }

func (e *EVMIndexer) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	var latest uint64
	err := e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
		n, err := c.GetBlockNumber(ctx)
		if err != nil {
			return err
		}
		latest = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int64(latest), nil
}

func (e *EVMIndexer) GetBlock(ctx context.Context, number int64) (*core.Block, error) {
	var block *core.Block
	hexNum := fmt.Sprintf("0x%x", uint64(number))
	err := e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
		b, err := c.GetBlockByNumber(ctx, hexNum, true)
		if err != nil {
			return err
		}
		cb, err := convertEthBlockToCore("evm", b)
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

func (e *EVMIndexer) GetBlocks(ctx context.Context, from, to int64) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: to < from")
	}

	results := make([]BlockResult, 0, to-from+1)
	for n := from; n <= to; n++ {
		b, err := e.GetBlock(ctx, n)
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

func (e *EVMIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A light check by calling GetBlockNumber
	if _, err := e.GetLatestBlockNumber(ctx); err != nil {
		slog.Warn("EVM health check failed", "name", e.GetName(), "err", err)
		return false
	}
	return true
}

// convertEthBlockToCore converts rpc.EthBlock to core.Block
func convertEthBlockToCore(networkName string, eb *rpc.EthBlock) (*core.Block, error) {
	if eb == nil {
		return nil, fmt.Errorf("nil ethereum block")
	}

	num, err := parseHexUint64(eb.Number)
	if err != nil {
		return nil, fmt.Errorf("parse block number: %w", err)
	}
	ts, err := parseHexUint64(eb.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("parse block timestamp: %w", err)
	}

	txs := make([]core.Transaction, 0, len(eb.Transactions))
	for _, tx := range eb.Transactions {
		cTx, err := convertEthTxToCore(networkName, tx)
		if err != nil {
			// Skip malformed tx but continue other txs
			slog.Warn("failed to convert transaction", "hash", tx.Hash, "err", err)
			continue
		}
		cTx.BlockNumber = num
		cTx.Timestamp = ts
		txs = append(txs, cTx)
	}

	return &core.Block{
		Number:       num,
		Hash:         eb.Hash,
		ParentHash:   eb.ParentHash,
		Timestamp:    ts,
		Transactions: txs,
	}, nil
}

// convertEthTxToCore converts rpc.EthTransaction to core.Transaction
func convertEthTxToCore(networkName string, tx rpc.EthTransaction) (core.Transaction, error) {
	// Value (wei)
	valueWei, err := parseHexBigInt(tx.Value)
	if err != nil {
		return core.Transaction{}, fmt.Errorf("parse value: %w", err)
	}
	// Fee = gas * gasPrice (wei), if available
	var fee decimal.Decimal
	gasWei, errGas := parseHexBigInt(tx.Gas)
	gasPriceWei, errGP := parseHexBigInt(tx.GasPrice)
	if errGas == nil && errGP == nil {
		feeWei := new(big.Int).Mul(gasWei, gasPriceWei)
		fee = decimal.NewFromBigInt(feeWei, 0)
	} else {
		fee = decimal.Zero
	}

	to := tx.To
	// Some txs (contract creation) have empty or null `to`
	if strings.TrimSpace(to) == "" {
		to = ""
	}

	// Type heuristic: empty input => transfer
	typ := "contract_call"
	if tx.Input == "0x" || tx.Input == "" {
		typ = "transfer"
	}

	return core.Transaction{
		TxHash:       tx.Hash,
		NetworkId:    networkName,
		FromAddress:  tx.From,
		ToAddress:    to,
		AssetAddress: "",                // native asset
		Amount:       valueWei.String(), // in wei
		Type:         typ,
		TxFee:        fee, // in wei
	}, nil
}

func parseHexUint64(h string) (uint64, error) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "0x")
	if h == "" {
		return 0, fmt.Errorf("empty hex string")
	}
	return strconv.ParseUint(h, 16, 64)
}

func parseHexBigInt(h string) (*big.Int, error) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "0x")
	if h == "" {
		return big.NewInt(0), nil
	}
	bi := new(big.Int)
	_, ok := bi.SetString(h, 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex: %s", h)
	}
	return bi, nil
}
