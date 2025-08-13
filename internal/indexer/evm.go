package indexer

import (
	"context"
	"fmt"
	"maps"
	"math/big"
	"strconv"
	"strings"
	"time"

	"idx/internal/common/ratelimiter"
	"idx/internal/core"
	"idx/internal/rpc"

	"github.com/shopspring/decimal"
)

const (
	ERC20_TRANSFER_TOPIC    = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	ERC20_TRANSFER_SIG      = "0xa9059cbb"
	ERC20_TRANSFER_FROM_SIG = "0x23b872dd"
)

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
		name := fmt.Sprintf("evm-%s-%d", config.Name, i)
		if err := fm.AddEthereumProvider(name, nodeURL.URL, nil, rl); err != nil {
			return nil, fmt.Errorf("add provider: %w", err)
		}
	}

	return &EVMIndexer{config: config, failover: fm, rateLimiter: rl}, nil
}

func (e *EVMIndexer) GetName() string { return e.config.Name }

func (e *EVMIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
		n, err := c.GetBlockNumber(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (e *EVMIndexer) GetBlock(ctx context.Context, number uint64) (*core.Block, error) {
	// Fetch block
	var eb *rpc.EthBlock
	hexNum := fmt.Sprintf("0x%x", number)
	if err := e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
		var err error
		eb, err = c.GetBlockByNumber(ctx, hexNum, true)
		return err
	}); err != nil {
		return nil, err
	}

	// Fetch receipts if needed
	receipts := make(map[string]*rpc.EthTransactionReceipt)
	if len(eb.Transactions) > 0 {
		var txHashes []string
		for _, tx := range eb.Transactions {
			if e.needsReceipt(tx) {
				txHashes = append(txHashes, tx.Hash)
			}
		}
		if len(txHashes) > 0 {
			_ = e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
				r, err := c.BatchGetTransactionReceipts(ctx, txHashes)
				if err == nil && r != nil {
					receipts = r
				}
				return nil
			})
		}
	}

	// Convert
	return e.convertBlock(eb, receipts)
}

func (e *EVMIndexer) GetBlocks(ctx context.Context, from, to uint64) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range")
	}

	var results []BlockResult
	var blocks map[uint64]*rpc.EthBlock

	// Prepare block numbers
	blockNums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		blockNums = append(blockNums, n)
	}

	// Try batch fetch
	if err := e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
		var err error
		blocks, err = c.BatchGetBlocksByNumber(ctx, blockNums, true)
		return err
	}); err != nil {
		if ferr := e.fallbackIndividual(ctx, from, to, &results); ferr != nil {
			return nil, ferr
		}
		return results, nil
	}

	// Collect tx hashes needing receipts
	var allTxHashes []string
	blockTxMap := make(map[uint64][]string)
	for blockNum, block := range blocks {
		if block == nil {
			continue
		}
		for _, tx := range block.Transactions {
			if e.needsReceipt(tx) {
				blockTxMap[blockNum] = append(blockTxMap[blockNum], tx.Hash)
				allTxHashes = append(allTxHashes, tx.Hash)
			}
		}
	}

	// Batch get all receipts
	allReceipts := make(map[string]*rpc.EthTransactionReceipt)
	if len(allTxHashes) > 0 {
		chunkSize := 50
		for i := 0; i < len(allTxHashes); i += chunkSize {
			end := min(i+chunkSize, len(allTxHashes))
			_ = e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
				if receipts, err := c.BatchGetTransactionReceipts(ctx, allTxHashes[i:end]); err == nil {
					maps.Copy(allReceipts, receipts)
				}
				return nil
			})
		}
	}

	// Process blocks
	results = make([]BlockResult, 0, int(to-from+1))
	for n := from; n <= to; n++ {
		block := blocks[n]
		if block == nil {
			results = append(results, BlockResult{
				Number: n, Block: nil,
				Error: &Error{ErrorType: ErrorTypeUnknown, Message: "block not found"},
			})
			continue
		}

		// Get receipts for this block
		blockReceipts := make(map[string]*rpc.EthTransactionReceipt)
		for _, txHash := range blockTxMap[n] {
			if receipt := allReceipts[txHash]; receipt != nil {
				blockReceipts[txHash] = receipt
			}
		}

		coreBlock, err := e.convertBlock(block, blockReceipts)
		if err != nil {
			results = append(results, BlockResult{
				Number: n, Block: nil,
				Error: &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
		} else {
			results = append(results, BlockResult{Number: n, Block: coreBlock})
		}
	}

	return results, nil
}

func (e *EVMIndexer) fallbackIndividual(ctx context.Context, from, to uint64, results *[]BlockResult) error {
	*results = make([]BlockResult, 0, int(to-from+1))
	for n := from; n <= to; n++ {
		hexNum := fmt.Sprintf("0x%x", n)
		var b *rpc.EthBlock
		if err := e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
			var err error
			b, err = c.GetBlockByNumber(ctx, hexNum, true)
			return err
		}); err != nil {
			*results = append(*results, BlockResult{
				Number: n, Block: nil,
				Error: &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
			continue
		}

		receipts := make(map[string]*rpc.EthTransactionReceipt)
		var txHashes []string
		for _, tx := range b.Transactions {
			if e.needsReceipt(tx) {
				txHashes = append(txHashes, tx.Hash)
			}
		}
		if len(txHashes) > 0 {
			_ = e.failover.ExecuteEthereumCall(ctx, func(c *rpc.EthereumClient) error {
				if r, err := c.BatchGetTransactionReceipts(ctx, txHashes); err == nil && r != nil {
					receipts = r
				}
				return nil
			})
		}

		block, err := e.convertBlock(b, receipts)
		if err != nil {
			*results = append(*results, BlockResult{
				Number: n, Block: nil,
				Error: &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()},
			})
		} else {
			*results = append(*results, BlockResult{Number: n, Block: block})
		}
	}
	return nil
}

func (e *EVMIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.GetLatestBlockNumber(ctx)
	return err == nil
}

func (e *EVMIndexer) convertBlock(eb *rpc.EthBlock, receipts map[string]*rpc.EthTransactionReceipt) (*core.Block, error) {
	num, _ := parseHexUint64(eb.Number)
	ts, _ := parseHexUint64(eb.Timestamp)

	var allTransfers []core.Transaction
	for _, tx := range eb.Transactions {
		receipt := receipts[tx.Hash]
		transfers := e.extractTransfers(tx, receipt, num, ts)
		allTransfers = append(allTransfers, transfers...)
	}

	return &core.Block{
		Number:       num,
		Hash:         eb.Hash,
		ParentHash:   eb.ParentHash,
		Timestamp:    ts,
		Transactions: allTransfers,
	}, nil
}

func (e *EVMIndexer) extractTransfers(tx rpc.EthTransaction, receipt *rpc.EthTransactionReceipt, blockNumber, ts uint64) []core.Transaction {
	var out []core.Transaction
	fee := e.calcFee(tx, receipt)

	// Native transfer
	if valueWei, err := parseHexBigInt(tx.Value); err == nil && valueWei.Cmp(big.NewInt(0)) > 0 && tx.To != "" {
		out = append(out, core.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    e.GetName(),
			BlockNumber:  blockNumber,
			FromAddress:  tx.From,
			ToAddress:    tx.To,
			AssetAddress: "",
			Amount:       valueWei.String(),
			Type:         "transfer",
			TxFee:        fee,
			Timestamp:    ts,
		})
	}

	// ERC20 transfers
	if receipt != nil {
		out = append(out, e.parseERC20Logs(tx.Hash, receipt.Logs, blockNumber, ts)...)
	} else if erc20 := e.parseERC20Input(tx, fee, blockNumber, ts); erc20 != nil {
		out = append(out, *erc20)
	}

	// Deduplicate exact same transfers within the same transaction
	if len(out) <= 1 {
		return out
	}
	seen := make(map[string]struct{}, len(out))
	unique := make([]core.Transaction, 0, len(out))
	for _, t := range out {
		key := t.TxHash + "|" + t.Type + "|" + t.AssetAddress + "|" + t.FromAddress + "|" + t.ToAddress + "|" + t.Amount + "|" + strconv.FormatUint(t.BlockNumber, 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, t)
	}
	return unique
}

func (e *EVMIndexer) parseERC20Logs(txHash string, logs []rpc.EthLog, blockNumber, ts uint64) []core.Transaction {
	var transfers []core.Transaction
	for _, log := range logs {
		if len(log.Topics) < 3 || log.Topics[0] != ERC20_TRANSFER_TOPIC {
			continue
		}

		from := "0x" + log.Topics[1][len(log.Topics[1])-40:]
		to := "0x" + log.Topics[2][len(log.Topics[2])-40:]
		amount, err := parseHexBigInt(log.Data)
		if err != nil {
			continue
		}

		transfers = append(transfers, core.Transaction{
			TxHash:       txHash,
			NetworkId:    e.GetName(),
			BlockNumber:  blockNumber,
			FromAddress:  from,
			ToAddress:    to,
			AssetAddress: log.Address,
			Amount:       amount.String(),
			Type:         "erc20_transfer",
			TxFee:        decimal.Zero,
			Timestamp:    ts,
		})
	}
	return transfers
}

func (e *EVMIndexer) parseERC20Input(tx rpc.EthTransaction, fee decimal.Decimal, blockNumber, ts uint64) *core.Transaction {
	if len(tx.Input) < 10 {
		return nil
	}

	sig := tx.Input[:10]
	switch sig {
	case ERC20_TRANSFER_SIG:
		// method(4 bytes) + 2 params (2 * 32 bytes) = 10 + 64 + 64 = 138 chars
		if len(tx.Input) < 138 {
			return nil
		}
		// First param (address) occupies [10:74] (64 hex chars). Keep last 40 hex chars
		to := "0x" + tx.Input[10:74][24:]
		// Second param (uint256 amount) occupies [74:138]
		amount, err := parseHexBigInt("0x" + tx.Input[74:138])
		if err != nil {
			return nil
		}
		return &core.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    e.GetName(),
			BlockNumber:  blockNumber,
			FromAddress:  tx.From,
			ToAddress:    to,
			AssetAddress: tx.To,
			Amount:       amount.String(),
			Type:         "erc20_transfer",
			TxFee:        fee,
			Timestamp:    ts,
		}
	case ERC20_TRANSFER_FROM_SIG:
		// method(4 bytes) + 3 params (3 * 32 bytes) = 10 + 64 + 64 + 64 = 202 chars
		if len(tx.Input) < 202 {
			return nil
		}
		// from address = first param [10:74], keep last 40 hex chars
		from := "0x" + tx.Input[10:74][24:]
		// to address = second param [74:138], keep last 40 hex chars
		to := "0x" + tx.Input[74:138][24:]
		// amount (uint256) = third param [138:202]
		amount, err := parseHexBigInt("0x" + tx.Input[138:202])
		if err != nil {
			return nil
		}
		return &core.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    e.GetName(),
			BlockNumber:  blockNumber,
			FromAddress:  from,
			ToAddress:    to,
			AssetAddress: tx.To,
			Amount:       amount.String(),
			Type:         "erc20_transfer",
			TxFee:        decimal.Zero,
			Timestamp:    ts,
		}
	}
	return nil
}

func (e *EVMIndexer) calcFee(tx rpc.EthTransaction, receipt *rpc.EthTransactionReceipt) decimal.Decimal {
	if receipt != nil {
		if gasUsed, err1 := parseHexBigInt(receipt.GasUsed); err1 == nil {
			if gasPrice, err2 := parseHexBigInt(receipt.EffectiveGasPrice); err2 == nil {
				return decimal.NewFromBigInt(new(big.Int).Mul(gasUsed, gasPrice), 0)
			}
		}
	}
	if gas, err1 := parseHexBigInt(tx.Gas); err1 == nil {
		if gasPrice, err2 := parseHexBigInt(tx.GasPrice); err2 == nil {
			return decimal.NewFromBigInt(new(big.Int).Mul(gas, gasPrice), 0)
		}
	}
	return decimal.Zero
}

func (e *EVMIndexer) needsReceipt(tx rpc.EthTransaction) bool {
	inputLen := len(strings.TrimSpace(tx.Input))
	if inputLen <= 2 {
		return false
	}
	if inputLen >= 10 {
		sig := tx.Input[:10]
		if sig == ERC20_TRANSFER_SIG || sig == ERC20_TRANSFER_FROM_SIG {
			return false
		}
	}
	return true
}

func parseHexUint64(h string) (uint64, error) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "0x")
	if h == "" {
		return 0, fmt.Errorf("empty hex")
	}
	return strconv.ParseUint(h, 16, 64)
}

func parseHexBigInt(h string) (*big.Int, error) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "0x")
	if h == "" {
		return big.NewInt(0), nil
	}
	bi := new(big.Int)
	if _, ok := bi.SetString(h, 16); !ok {
		return nil, fmt.Errorf("invalid hex: %s", h)
	}
	return bi, nil
}
