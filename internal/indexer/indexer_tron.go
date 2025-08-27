package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/btcsuite/btcutil/base58"
	"github.com/fystack/transaction-indexer/internal/rpc"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/ratelimiter"
	"github.com/shopspring/decimal"
)

// keccak256("Transfer(address,address,uint256)")
const ERC_TRANSFER_TOPIC = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// TRON denomination: 1 TRX = 1,000,000 sun
const TRX_SUN_DECIMALS = 1000000

type TronIndexer struct {
	config      config.ChainConfig
	failover    *rpc.FailoverManager
	rateLimiter *ratelimiter.PooledRateLimiter

	// Caches for frequently accessed data
	addrCache sync.Map // cache for address conversions
}

func NewTronIndexer(config config.ChainConfig) (*TronIndexer, error) {
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
		config:      config,
		failover:    fm,
		rateLimiter: rl,
	}, nil
}

func (t *TronIndexer) GetName() string { return t.config.Name.String() }

func (t *TronIndexer) GetAddressType() enum.AddressType {
	return enum.AddressTypeTron
}
func (t *TronIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
		n, err := c.GetBlockNumber(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (t *TronIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
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

func (t *TronIndexer) processBlock(tronBlock *rpc.TronBlock, txns []*rpc.TronTransactionInfo) (*types.Block, error) {
	// Pre-allocate slices with estimated capacity
	estimatedTxCount := len(txns) * 2 // rough estimate
	transactions := make([]types.Transaction, 0, estimatedTxCount)

	// Index tx info by txID for O(1) lookup
	infoByID := make(map[string]*rpc.TronTransactionInfo, len(txns))
	for _, ti := range txns {
		if ti != nil && ti.ID != "" {
			infoByID[ti.ID] = ti
		}
	}

	block := &types.Block{
		Number:       uint64(tronBlock.BlockHeader.RawData.Number),
		Hash:         tronBlock.BlockID,
		ParentHash:   tronBlock.BlockHeader.RawData.ParentHash,
		Timestamp:    convertTronTimestamp(tronBlock.BlockHeader.RawData.Timestamp),
		Transactions: transactions,
	}

	// Process transaction info logs first
	feeAssigned := make(map[string]bool, len(txns))

	for _, tx := range txns {
		if tx == nil {
			continue
		}

		// Calculate total fee including resource consumption
		fee := t.calculateTotalFee(tx)

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
		// Check transaction success status
		if !t.isTransactionSuccessful(&rawTx) {
			slog.Debug("Skipping failed transaction", "txid", rawTx.TxID)
			continue
		}

		// Get transaction info or use defaults
		ts := convertTronTimestamp(tronBlock.BlockHeader.RawData.Timestamp)
		blkNum := uint64(tronBlock.BlockHeader.RawData.Number)
		fee := decimal.Zero

		if ti := infoByID[rawTx.TxID]; ti != nil {
			ts = convertTronTimestamp(ti.BlockTimestamp)
			blkNum = uint64(ti.BlockNumber)
			// Calculate total fee including resource consumption
			fee = t.calculateTotalFee(ti)
		}

		for _, contract := range rawTx.RawData.Contract {
			var tr *types.Transaction
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

			slog.Info("Processed transaction", "contract", contract.Type, "transaction", tr)
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

func (t *TronIndexer) processTransferContract(param *rpc.TronContractParameter, txID string, blockNum, timestamp uint64) (*types.Transaction, error) {
	var transfer rpc.TronTransferContract
	if err := json.Unmarshal(param.Value, &transfer); err != nil {
		return nil, fmt.Errorf("failed to parse TransferContract: %w", err)
	}

	return &types.Transaction{
		TxHash:       txID,
		NetworkId:    t.GetName(),
		BlockNumber:  blockNum,
		FromAddress:  t.tronToHexAddressCached(transfer.OwnerAddress),
		ToAddress:    t.tronToHexAddressCached(transfer.ToAddress),
		AssetAddress: "",
		Amount:       decimal.NewFromInt(transfer.Amount).String(),
		Type:         "transfer",
		Timestamp:    timestamp,
	}, nil
}

func (t *TronIndexer) processTransferAssetContract(param *rpc.TronContractParameter, txID string, blockNum, timestamp uint64) (*types.Transaction, error) {
	var asset rpc.TronTransferAssetContract
	if err := json.Unmarshal(param.Value, &asset); err != nil {
		return nil, fmt.Errorf("failed to parse TransferAssetContract: %w", err)
	}

	return &types.Transaction{
		TxHash:       txID,
		NetworkId:    t.GetName(),
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
func (t *TronIndexer) parseTRC20Logs(tx *rpc.TronTransactionInfo) []types.Transaction {
	if len(tx.Log) == 0 {
		return nil
	}

	transfers := make([]types.Transaction, 0, len(tx.Log))

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

		transfers = append(transfers, types.Transaction{
			TxHash:       tx.ID,
			NetworkId:    t.GetName(),
			BlockNumber:  uint64(tx.BlockNumber),
			FromAddress:  t.tronNormalizeHexOrBase58(from),
			ToAddress:    t.tronNormalizeHexOrBase58(to),
			AssetAddress: t.tronNormalizeHexOrBase58(log.Address),
			Amount:       amount.String(),
			Type:         "erc20_transfer",
			TxFee:        decimal.Zero, // Will be set in processBlock with total fee
			Timestamp:    convertTronTimestamp(tx.BlockTimestamp),
		})
	}
	return transfers
}

func (t *TronIndexer) GetBlocks(ctx context.Context, start, end uint64) ([]BlockResult, error) {
	blockNumbers := make([]uint64, 0, end-start+1)
	for i := start; i <= end; i++ {
		blockNumbers = append(blockNumbers, i)
	}
	return t.GetBlocksByNumbers(ctx, blockNumbers)
}

func (t *TronIndexer) GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	return t.getBlocks(ctx, blockNumbers)
}

func (t *TronIndexer) getBlocks(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}

	blocks := make([]BlockResult, len(blockNumbers))

	const maxWorkers = 10
	workers := len(blockNumbers)
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
		for i, num := range blockNumbers {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{blockNum: num, index: i}:
			}
		}
	}()

	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// if want to fail when there is an error
	var firstErr error
	for _, block := range blocks {
		if block.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", block.Number, block.Error.Message)
			break
		}
	}

	return blocks, firstErr
}

func (t *TronIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := t.GetLatestBlockNumber(ctx)
	return err == nil
}

// ExecuteTronCall exposes the failover functionality for debugging
func (t *TronIndexer) ExecuteTronCall(ctx context.Context, fn func(*rpc.TronClient) error) error {
	return t.failover.ExecuteTronCall(ctx, fn)
}

func (t *TronIndexer) tronNormalizeHexOrBase58(addr string) string {
	return t.tronToHexAddress(addr)
}

// tronToHexAddress converts addresses to Tron base58 format
func (t *TronIndexer) tronToHexAddress(addr string) string {
	cleaned := strings.TrimSpace(addr)

	// Keep existing Tron base58 addresses
	if len(cleaned) >= 34 && (cleaned[0] == 'T' || cleaned[0] == 't') {
		return cleaned
	}

	// Convert hex addresses to Tron format
	if strings.HasPrefix(cleaned, "0x") {
		return t.evmToTronAddress(cleaned)
	}

	// Handle hex without 0x prefix
	if (len(cleaned) == 40 || len(cleaned) == 42) && isHex(cleaned) {
		return t.evmToTronAddress("0x" + strings.ToLower(cleaned))
	}

	// Fallback: assume hex and convert
	return t.evmToTronAddress("0x" + cleaned)
}

// evmToTronAddress converts 0x41... hex -> T... base58
func (t *TronIndexer) evmToTronAddress(evmAddr string) string {
	hexAddr := strings.TrimPrefix(strings.ToLower(evmAddr), "0x")

	// must be 42 chars (0x + 41 prefix + 20 bytes) or 40 (just 20 bytes)
	if len(hexAddr) != 40 && len(hexAddr) != 42 {
		return evmAddr
	}

	// decode 20 bytes (last 40 hex chars)
	raw, err := hex.DecodeString(hexAddr[len(hexAddr)-40:])
	if err != nil {
		return evmAddr
	}

	// prepend Tron network prefix 0x41
	payload := append([]byte{0x41}, raw...)

	// checksum = sha256d[:4]
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	checksum := h2[:4]

	full := append(payload, checksum...)
	return base58.Encode(full)
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

// calculateTotalFee calculates the total fee including energy and bandwidth costs
func (t *TronIndexer) calculateTotalFee(txInfo *rpc.TronTransactionInfo) decimal.Decimal {
	if txInfo == nil {
		slog.Debug("calculateTotalFee: txInfo is nil")
		return decimal.Zero
	}

	// Base transaction fee
	totalFee := decimal.NewFromInt(txInfo.Fee)
	if txInfo.EnergyFee > 0 {
		totalFee = totalFee.Add(decimal.NewFromInt(txInfo.EnergyFee))
	}

	// Add net fee (bandwidth fee)
	if txInfo.NetFee > 0 {
		totalFee = totalFee.Add(decimal.NewFromInt(txInfo.NetFee))
	}

	// Check if top-level fees are all zero, then use receipt fees as fallback
	topLevelFeesZero := (txInfo.Fee + txInfo.EnergyFee + txInfo.NetFee) == 0
	if topLevelFeesZero && txInfo.Receipt.EnergyFee > 0 {
		totalFee = totalFee.Add(decimal.NewFromInt(txInfo.Receipt.EnergyFee))
	}
	if topLevelFeesZero && txInfo.Receipt.NetFee > 0 {
		totalFee = totalFee.Add(decimal.NewFromInt(txInfo.Receipt.NetFee))
	}

	// Add energy penalty if any
	if txInfo.Receipt.EnergyPenaltyTotal > 0 {
		totalFee = totalFee.Add(decimal.NewFromInt(txInfo.Receipt.EnergyPenaltyTotal))
	}

	totalFeeTRX := totalFee.Div(decimal.NewFromInt(TRX_SUN_DECIMALS))
	return totalFeeTRX
}

// convertTronTimestamp converts TRON millisecond timestamp to Unix seconds
func convertTronTimestamp(tronTimestamp int64) uint64 {
	return uint64(tronTimestamp / 1000)
}

// isTransactionSuccessful checks if a transaction was successful
// Safely handles cases where ret array might be empty
func (t *TronIndexer) isTransactionSuccessful(tx *rpc.TronTransaction) bool {
	if tx == nil || len(tx.Ret) == 0 {
		// If no ret info, assume successful (this happens with simple transfers)
		return true
	}

	// Check the first result entry
	firstRet := tx.Ret[0]
	return firstRet.ContractRet == "SUCCESS" || firstRet.ContractRet == ""
}
