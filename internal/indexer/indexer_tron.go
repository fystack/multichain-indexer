package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/common/ratelimiter"
	"github.com/fystack/transaction-indexer/internal/core"
	"github.com/fystack/transaction-indexer/internal/rpc"

	"github.com/shopspring/decimal"
)

// keccak256("Transfer(address,address,uint256)")
const ERC_TRANSFER_TOPIC = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type TronIndexer struct {
	chainName   string
	config      core.ChainConfig
	failover    *rpc.FailoverManager
	rateLimiter *ratelimiter.PooledRateLimiter

	// Caches for frequently accessed data
	addrCache sync.Map // cache for address conversions
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
		auth := rpc.NodeToAuthConfig(nodeURL)
		if err := fm.AddTronProvider(name, nodeURL.URL, auth, rl); err != nil {
			return nil, fmt.Errorf("add provider %q: %w", name, err)
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
		n, err := c.GetBlockNumber(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (t *TronIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*core.Block, error) {
	var (
		wg        sync.WaitGroup
		tronBlock *rpc.TronBlock
		txns      []*rpc.TronTransactionInfo
		blockErr  error
		txnErr    error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		blockErr = t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
			b, err := c.GetBlockByNumber(ctx, fmt.Sprintf("%d", blockNumber), true)
			if err == nil {
				tronBlock = b
			}
			return err
		})
	}()

	go func() {
		defer wg.Done()
		txnErr = t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
			r, err := c.BatchGetTransactionReceiptsByBlockNum(ctx, int64(blockNumber))
			if err == nil {
				txns = r
			}
			return err
		})
	}()

	wg.Wait()

	if blockErr != nil {
		return nil, blockErr
	}
	if txnErr != nil {
		return nil, txnErr
	}
	return t.processBlock(tronBlock, txns)
}

func (t *TronIndexer) processBlock(tronBlock *rpc.TronBlock, txns []*rpc.TronTransactionInfo) (*core.Block, error) {
	// Pre-allocate slices with estimated capacity
	estimatedTxCount := len(txns) * 2 // rough estimate
	transactions := make([]core.Transaction, 0, estimatedTxCount)

	// Index tx info by txID for O(1) lookup
	infoByID := make(map[string]*rpc.TronTransactionInfo, len(txns))
	for _, ti := range txns {
		if ti != nil && ti.ID != "" {
			infoByID[ti.ID] = ti
		}
	}

	block := &core.Block{
		Number:       uint64(tronBlock.BlockHeader.RawData.Number),
		Hash:         tronBlock.BlockID,
		ParentHash:   tronBlock.BlockHeader.RawData.ParentHash,
		Timestamp:    uint64(tronBlock.BlockHeader.RawData.Timestamp),
		Transactions: transactions,
	}

	// Process transaction info logs first
	feeAssigned := make(map[string]bool, len(txns))

	for _, tx := range txns {
		if tx == nil {
			continue
		}

		fee := decimal.NewFromInt(tx.Fee)

		// Process TRC20/721 logs
		if len(tx.Log) > 0 {
			transfers := t.parseTRC20Logs(tx)
			if len(transfers) > 0 {
				// Assign fee to first transfer of this transaction
				transfers[0].TxFee = fee
				feeAssigned[tx.ID] = true
				block.Transactions = append(block.Transactions, transfers...)
			}
		}
	}

	// Process top-level transactions from the block
	for _, rawTx := range tronBlock.Transactions {
		// Get transaction info or use defaults
		ts := uint64(tronBlock.BlockHeader.RawData.Timestamp)
		blkNum := uint64(tronBlock.BlockHeader.RawData.Number)
		fee := decimal.Zero

		if ti := infoByID[rawTx.TxID]; ti != nil {
			ts = uint64(ti.BlockTimestamp)
			blkNum = uint64(ti.BlockNumber)
			fee = decimal.NewFromInt(ti.Fee)
		}

		for _, contract := range rawTx.RawData.Contract {
			var tr *core.Transaction
			var err error

			switch contract.Type {
			case "TransferContract":
				tr, err = t.processTransferContract(&contract.Parameter, rawTx.TxID, blkNum, ts)
			case "TransferAssetContract":
				tr, err = t.processTransferAssetContract(&contract.Parameter, rawTx.TxID, blkNum, ts)
			default:
				continue
			}

			if err != nil {
				continue // Skip malformed contracts
			}

			// Assign fee only if not already assigned to this transaction
			if !feeAssigned[rawTx.TxID] {
				tr.TxFee = fee
				feeAssigned[rawTx.TxID] = true
			}

			block.Transactions = append(block.Transactions, *tr)
		}
	}

	return block, nil
}

func (t *TronIndexer) processTransferContract(param *rpc.TronContractParameter, txID string, blockNum, timestamp uint64) (*core.Transaction, error) {
	var transfer rpc.TronTransferContract
	if err := json.Unmarshal(param.Value, &transfer); err != nil {
		return nil, fmt.Errorf("failed to parse TransferContract: %w", err)
	}

	return &core.Transaction{
		TxHash:       txID,
		NetworkId:    t.chainName,
		BlockNumber:  blockNum,
		FromAddress:  t.tronToHexAddressCached(transfer.OwnerAddress),
		ToAddress:    t.tronToHexAddressCached(transfer.ToAddress),
		AssetAddress: "",
		Amount:       decimal.NewFromInt(transfer.Amount).String(),
		Type:         "transfer",
		Timestamp:    timestamp,
	}, nil
}

func (t *TronIndexer) processTransferAssetContract(param *rpc.TronContractParameter, txID string, blockNum, timestamp uint64) (*core.Transaction, error) {
	var asset rpc.TronTransferAssetContract
	if err := json.Unmarshal(param.Value, &asset); err != nil {
		return nil, fmt.Errorf("failed to parse TransferAssetContract: %w", err)
	}

	return &core.Transaction{
		TxHash:       txID,
		NetworkId:    t.chainName,
		BlockNumber:  blockNum,
		FromAddress:  t.tronToHexAddressCached(asset.OwnerAddress),
		ToAddress:    t.tronToHexAddressCached(asset.ToAddress),
		AssetAddress: asset.AssetName,
		Amount:       decimal.NewFromInt(asset.Amount).String(),
		Type:         "trc10_transfer",
		Timestamp:    timestamp,
	}, nil
}

// tronToHexAddressCached caches address conversions to avoid repeated computation
func (t *TronIndexer) tronToHexAddressCached(tronAddr string) string {
	if cached, ok := t.addrCache.Load(tronAddr); ok {
		return cached.(string)
	}

	hexAddr := t.tronToHexAddress(tronAddr)
	t.addrCache.Store(tronAddr, hexAddr)
	return hexAddr
}

// parseTRC20Logs converts Tron logs that represent ERC-20-compatible Transfer events
func (t *TronIndexer) parseTRC20Logs(tx *rpc.TronTransactionInfo) []core.Transaction {
	if len(tx.Log) == 0 {
		return nil
	}

	transfers := make([]core.Transaction, 0, len(tx.Log))

	for _, log := range tx.Log {
		if len(log.Topics) < 3 {
			continue
		}
		topic0 := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(log.Topics[0])), "0x")
		if topic0 != ERC_TRANSFER_TOPIC {
			continue
		}

		from, ok1 := extractAddressFromTopic(log.Topics[1])
		to, ok2 := extractAddressFromTopic(log.Topics[2])
		if !ok1 || !ok2 {
			continue
		}
		amount, ok := parseHexToBigInt(log.Data)
		if !ok {
			continue
		}

		transfers = append(transfers, core.Transaction{
			TxHash:       tx.ID,
			NetworkId:    t.chainName,
			BlockNumber:  uint64(tx.BlockNumber),
			FromAddress:  from,
			ToAddress:    to,
			AssetAddress: t.tronNormalizeHexOrBase58(log.Address),
			Amount:       amount.String(),
			Type:         "erc20_transfer",
			TxFee:        decimal.Zero,
			Timestamp:    uint64(tx.BlockTimestamp),
		})
	}
	return transfers
}

func (t *TronIndexer) GetBlocks(ctx context.Context, start, end uint64) ([]BlockResult, error) {
	if start > end {
		return nil, fmt.Errorf("start block (%d) > end block (%d)", start, end)
	}

	count := end - start + 1
	blocks := make([]BlockResult, count)

	const maxWorkers = 10
	workers := int(count)
	if workers > maxWorkers {
		workers = maxWorkers
	}

	type job struct {
		blockNum uint64
		index    int
	}

	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	// workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j, ok := <-jobs:
					if !ok {
						return
					}
					blk, err := t.GetBlock(ctx, j.blockNum)
					blocks[j.index] = BlockResult{
						Number: j.blockNum,
						Block:  blk,
					}
					if err != nil {
						blocks[j.index].Error = &Error{
							ErrorType: ErrorTypeUnknown,
							Message:   err.Error(),
						}
					}
				}
			}
		}()
	}

	// producer
	go func() {
		defer close(jobs)
		for i := uint64(0); i < count; i++ {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{blockNum: start + i, index: int(i)}:
			}
		}
	}()

	wg.Wait()

	// short-circuit on ctx
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Check for any errors
	for _, block := range blocks {
		if block.Error != nil {
			return blocks, fmt.Errorf("block %d: %s", block.Number, block.Error.Message)
		}
	}

	return blocks, nil
}

func (t *TronIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := t.GetLatestBlockNumber(ctx)
	return err == nil
}

func (t *TronIndexer) tronNormalizeHexOrBase58(addr string) string {
	a := strings.TrimSpace(addr)
	// heuristic: TRON base58 addresses start with 'T' and are ~34 chars
	if len(a) >= 34 && (a[0] == 'T' || a[0] == 't') {
		return t.tronToHexAddressCached(a)
	}
	// assume hex; add 0x if missing, lowercase
	a = strings.TrimPrefix(strings.ToLower(a), "0x")
	if a == "" {
		return "0x0"
	}
	return "0x" + a
}

// tronToHexAddress converts a TRON base58 address to hex
func (t *TronIndexer) tronToHexAddress(tronAddr string) string {
	// This is a simplified implementation
	// In a real implementation, you would need to:
	// 1. Base58 decode the TRON address
	// 2. Remove the checksum (last 4 bytes)
	// 3. Remove the network byte (first byte, usually 0x41 for mainnet)
	// 4. Convert the remaining 20 bytes to hex with 0x prefix

	// For now, if it's already hex-like, return it
	cleaned := strings.TrimSpace(tronAddr)
	if strings.HasPrefix(cleaned, "0x") || (len(cleaned) == 40 && isHex(cleaned)) {
		if !strings.HasPrefix(cleaned, "0x") {
			cleaned = "0x" + cleaned
		}
		return strings.ToLower(cleaned)
	}

	// If it's a TRON base58 address, you'd need proper base58 decoding
	// This is a placeholder - implement proper TRON address conversion
	return "0x" + strings.ToLower(tronAddr)
}

// isHex checks if a string contains only hex characters
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// extractAddressFromTopic extracts address from 32-byte topic (last 20 bytes)
func extractAddressFromTopic(topic string) (string, bool) {
	s := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(topic, "0x")))
	if len(s) != 64 {
		return "", false
	}
	// optional: validate hex
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", false
		}
	}
	return "0x" + s[24:], true // last 20 bytes
}

// parseHexToBigInt parses hex string to big.Int more efficiently
func parseHexToBigInt(hexStr string) (*big.Int, bool) {
	hexStr = strings.TrimSpace(hexStr)
	hexStr = strings.TrimPrefix(hexStr, "0x")

	if hexStr == "" {
		return big.NewInt(0), true
	}

	amount := new(big.Int)
	if _, ok := amount.SetString(hexStr, 16); !ok {
		return nil, false
	}

	return amount, true
}
