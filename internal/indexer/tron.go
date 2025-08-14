package indexer

import (
	"context"
	"fmt"
	"idx/internal/common/ratelimiter"
	"idx/internal/core"
	"idx/internal/rpc"
	"math/big"
	"strings"
	"sync"
	"time"

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
		if err := fm.AddTronProvider(name, nodeURL.URL, nil, rl); err != nil {
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
		n, err := c.GetNowBlock(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (t *TronIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*core.Block, error) {
	// Fetch block and transaction info concurrently
	type blockData struct {
		block *rpc.TronBlock
		txns  []*rpc.TronTransactionInfo
		err   error
	}

	blockCh := make(chan blockData, 1)

	go func() {
		var tronBlock *rpc.TronBlock
		var txns []*rpc.TronTransactionInfo
		var blockErr, txnErr error

		// Fetch block metadata and transaction infos concurrently
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			blockErr = t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
				b, err := c.GetBlockByNumber(ctx, fmt.Sprintf("%d", blockNumber), true)
				if err != nil {
					return err
				}
				tronBlock = b
				return nil
			})
		}()

		go func() {
			defer wg.Done()
			txnErr = t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
				var err error
				txns, err = c.GetTransactionInfoByBlockNum(ctx, int64(blockNumber))
				return err
			})
		}()

		wg.Wait()

		// Return first error encountered
		if blockErr != nil {
			blockCh <- blockData{err: blockErr}
			return
		}
		if txnErr != nil {
			blockCh <- blockData{err: txnErr}
			return
		}

		blockCh <- blockData{block: tronBlock, txns: txns}
	}()

	select {
	case data := <-blockCh:
		if data.err != nil {
			return nil, data.err
		}
		return t.processBlock(data.block, data.txns)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

	// Process transaction info logs and internal transactions
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

		// Process internal transactions (TRX & TRC10)
		for _, itx := range tx.InternalTransactions {
			if len(itx.CallValueInfo) == 0 {
				continue
			}

			from := t.tronToHexAddressCached(itx.CallerAddress)
			to := t.tronToHexAddressCached(itx.TransferToAddress)

			for i, v := range itx.CallValueInfo {
				amount := decimal.NewFromInt(v.CallValue)

				tr := core.Transaction{
					TxHash:      tx.ID,
					NetworkId:   t.chainName,
					BlockNumber: uint64(tx.BlockNumber),
					FromAddress: from,
					ToAddress:   to,
					Amount:      amount.String(),
					Timestamp:   uint64(tx.BlockTimestamp),
				}

				if strings.TrimSpace(v.TokenName) == "" {
					tr.Type = "transfer"
					tr.AssetAddress = ""
				} else {
					tr.Type = "trc10_transfer"
					tr.AssetAddress = v.TokenName
				}

				// Assign fee only to first transaction of this txID
				if i == 0 && !feeAssigned[tx.ID] {
					tr.TxFee = fee
					feeAssigned[tx.ID] = true
				}

				block.Transactions = append(block.Transactions, tr)
			}
		}
	}

	// Process top-level transactions
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
	transfer, err := param.ParseTransferContract()
	if err != nil {
		return nil, err
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
	asset, err := param.ParseTransferAssetContract()
	if err != nil {
		return nil, err
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

	hexAddr := rpc.TronToHexAddress(tronAddr)
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

		// Normalize topic for comparison (remove 0x prefix)
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
			AssetAddress: t.tronToHexAddressCached(log.Address),
			Amount:       amount.String(),
			Type:         "erc20_transfer",
			TxFee:        decimal.Zero, // Fee assigned later
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

	// Use worker pool for concurrent processing
	const maxWorkers = 10
	workers := int(count)
	if workers > maxWorkers {
		workers = maxWorkers
	}

	type job struct {
		blockNum uint64
		index    int
	}

	jobs := make(chan job, count)
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				block, _ := t.GetBlock(ctx, job.blockNum)
				blocks[job.index] = BlockResult{
					Number: job.blockNum,
					Block:  block,
					Error:  nil,
				}
			}
		}()
	}

	// Send jobs
	go func() {
		defer close(jobs)
		for i := uint64(0); i < count; i++ {
			blockNum := start + i
			select {
			case jobs <- job{blockNum: blockNum, index: int(i)}:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()

	// Check for context cancellation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return blocks, nil
}

func (t *TronIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := t.GetLatestBlockNumber(ctx)
	return err == nil
}

// extractAddressFromTopic extracts address from 32-byte topic (last 20 bytes)
func extractAddressFromTopic(topic string) (string, bool) {
	topic = strings.TrimSpace(topic)
	topic = strings.TrimPrefix(strings.ToLower(topic), "0x")

	if len(topic) < 40 {
		return "", false
	}

	// Extract last 40 hex chars (20 bytes) for address
	if len(topic) >= 64 {
		return "0x" + topic[len(topic)-40:], true
	}

	// Fallback for already 20-byte format
	return "0x" + topic, true
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
