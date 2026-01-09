package indexer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/bitcoin"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

type BitcoinIndexer struct {
	chainName     string
	config        config.ChainConfig
	failover      *rpc.Failover[bitcoin.BitcoinAPI]
	pubkeyStore   PubkeyStore
	confirmations uint64
}

func NewBitcoinIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[bitcoin.BitcoinAPI],
	pubkeyStore PubkeyStore,
) *BitcoinIndexer {
	confirmations := uint64(6) // Default 6 confirmations for Bitcoin
	if cfg.Confirmations > 0 {
		confirmations = cfg.Confirmations
	}

	return &BitcoinIndexer{
		chainName:     chainName,
		config:        cfg,
		failover:      failover,
		pubkeyStore:   pubkeyStore,
		confirmations: confirmations,
	}
}

func (b *BitcoinIndexer) GetName() string {
	return strings.ToUpper(b.chainName)
}

func (b *BitcoinIndexer) GetNetworkType() enum.NetworkType {
	return enum.NetworkTypeBtc
}

func (b *BitcoinIndexer) GetNetworkInternalCode() string {
	return b.config.InternalCode
}

func (b *BitcoinIndexer) GetNetworkId() string {
	return b.config.NetworkId
}

func (b *BitcoinIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := b.failover.ExecuteWithRetry(ctx, func(c bitcoin.BitcoinAPI) error {
		n, err := c.GetBlockCount(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (b *BitcoinIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	var btcBlock *bitcoin.Block

	err := b.failover.ExecuteWithRetry(ctx, func(c bitcoin.BitcoinAPI) error {
		// Verbosity 2 = full transaction details (prevout may or may not be included)
		block, err := c.GetBlockByHeight(ctx, number, 2)
		if err != nil {
			return err
		}
		btcBlock = block
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", number, err)
	}

	return b.convertBlockWithPrevoutResolution(ctx, btcBlock)
}

// convertBlockWithPrevoutResolution converts a block and resolves prevout data for monitored transactions
func (b *BitcoinIndexer) convertBlockWithPrevoutResolution(ctx context.Context, btcBlock *bitcoin.Block) (*types.Block, error) {
	var allTransfers []types.Transaction

	// Calculate latest block height from confirmations
	latestBlock := btcBlock.Height
	if btcBlock.Confirmations > 0 {
		latestBlock = btcBlock.Height + btcBlock.Confirmations - 1
	}

	// Get a client for prevout resolution
	provider, _ := b.failover.GetBestProvider()
	var btcClient *bitcoin.BitcoinClient
	if provider != nil {
		btcClient, _ = provider.Client.(*bitcoin.BitcoinClient)
	}

	for i := range btcBlock.Tx {
		tx := &btcBlock.Tx[i]

		// Skip coinbase transactions
		if tx.IsCoinbase() {
			continue
		}

		// Check if any output goes to a monitored address
		hasMonitoredOutput := false
		for _, vout := range tx.Vout {
			addr := bitcoin.GetOutputAddress(&vout)
			if addr != "" && b.pubkeyStore != nil && b.pubkeyStore.Exist(enum.NetworkTypeBtc, addr) {
				hasMonitoredOutput = true
				break
			}
		}

		// If we have a monitored output and need prevout data for FROM address
		if hasMonitoredOutput && btcClient != nil && len(tx.Vin) > 0 && tx.Vin[0].PrevOut == nil {
			// Resolve prevout for the first input only (for FROM address)
			if tx.Vin[0].TxID != "" {
				if resolved, err := btcClient.GetTransactionWithPrevouts(ctx, tx.TxID); err == nil {
					// Copy resolved prevout data
					for j := range tx.Vin {
						if j < len(resolved.Vin) && resolved.Vin[j].PrevOut != nil {
							tx.Vin[j].PrevOut = resolved.Vin[j].PrevOut
						}
					}
				}
			}
		}

		// Extract transfers
		transfers := b.extractTransfersFromTx(tx, btcBlock.Height, btcBlock.Time, latestBlock)
		allTransfers = append(allTransfers, transfers...)
	}

	return &types.Block{
		Number:       btcBlock.Height,
		Hash:         btcBlock.Hash,
		ParentHash:   btcBlock.PreviousBlockHash,
		Timestamp:    btcBlock.Time,
		Transactions: allTransfers,
	}, nil
}

func (b *BitcoinIndexer) GetBlocks(
	ctx context.Context,
	from, to uint64,
	isParallel bool,
) ([]BlockResult, error) {
	blockNums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		blockNums = append(blockNums, n)
	}

	return b.GetBlocksByNumbers(ctx, blockNums)
}

func (b *BitcoinIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}

	// Bitcoin RPC doesn't support batching like Ethereum
	// Process blocks sequentially with concurrency control
	results := make([]BlockResult, len(blockNumbers))

	workers := len(blockNumbers)
	workers = min(workers, b.config.Throttle.Concurrency)

	type job struct {
		num   uint64
		index int
	}

	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				block, err := b.GetBlock(ctx, j.num)
				results[j.index] = BlockResult{Number: j.num, Block: block}
				if err != nil {
					results[j.index].Error = &Error{
						ErrorType: ErrorTypeUnknown,
						Message:   err.Error(),
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i, num := range blockNumbers {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{num: num, index: i}:
			}
		}
	}()

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var firstErr error
	for _, b := range results {
		if b.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", b.Number, b.Error.Message)
			break
		}
	}
	return results, firstErr
}

// convertBlock converts a Bitcoin block without prevout resolution (fast path)
// Use convertBlockWithPrevoutResolution for full address resolution
func (b *BitcoinIndexer) convertBlock(btcBlock *bitcoin.Block) (*types.Block, error) {
	var allTransfers []types.Transaction

	// Calculate latest block height from confirmations
	latestBlock := btcBlock.Height
	if btcBlock.Confirmations > 0 {
		latestBlock = btcBlock.Height + btcBlock.Confirmations - 1
	}

	for i := range btcBlock.Tx {
		tx := &btcBlock.Tx[i]
		if tx.IsCoinbase() {
			continue
		}
		transfers := b.extractTransfersFromTx(tx, btcBlock.Height, btcBlock.Time, latestBlock)
		allTransfers = append(allTransfers, transfers...)
	}

	return &types.Block{
		Number:       btcBlock.Height,
		Hash:         btcBlock.Hash,
		ParentHash:   btcBlock.PreviousBlockHash,
		Timestamp:    btcBlock.Time,
		Transactions: allTransfers,
	}, nil
}

// extractTransfersFromTx extracts relevant transfers for monitored addresses from a transaction.
// This is the main extraction function that works with transaction data that may or may not have prevout.
// Only extracts incoming transfers (TO monitored addresses) - outgoing transfers are handled by withdrawal flow.
func (b *BitcoinIndexer) extractTransfersFromTx(
	tx *bitcoin.Transaction,
	blockNumber, ts, latestBlock uint64,
) []types.Transaction {
	var transfers []types.Transaction

	// Skip coinbase transactions
	if tx.IsCoinbase() {
		return transfers
	}

	// Calculate fee (will be zero if prevout data is missing)
	fee := tx.CalculateFee()

	// Track monitored output addresses (TO addresses for deposits)
	monitoredOutputs := make(map[string]bool)

	// Identify monitored outputs (incoming/receiving)
	for _, vout := range tx.Vout {
		outputAddr := bitcoin.GetOutputAddress(&vout)
		if outputAddr != "" && b.pubkeyStore != nil && b.pubkeyStore.Exist(enum.NetworkTypeBtc, outputAddr) {
			monitoredOutputs[outputAddr] = true
		}
	}

	// Early return if no monitored TO addresses found
	if len(monitoredOutputs) == 0 {
		return transfers
	}

	// Calculate confirmations once
	confirmations := b.calculateConfirmations(blockNumber, latestBlock)
	status := types.CalculateStatus(confirmations)

	// Extract incoming transfers (TO monitored addresses only)
	feeAssigned := false
	for _, vout := range tx.Vout {
		toAddr := bitcoin.GetOutputAddress(&vout)
		if toAddr == "" || !monitoredOutputs[toAddr] {
			continue
		}

		// Get "from" address (first input address with prevout)
		fromAddr := b.getFirstInputAddress(tx)

		// Convert BTC to satoshis (multiply by 1e8)
		amountSat := int64(vout.Value * 1e8)

		txFee := decimal.Zero
		if !feeAssigned {
			txFee = fee
			feeAssigned = true
		}

		transfer := types.Transaction{
			TxHash:        tx.TxID,
			NetworkId:     b.config.NetworkId,
			BlockNumber:   blockNumber,
			FromAddress:   fromAddr,
			ToAddress:     toAddr,
			AssetAddress:  "", // Empty for native BTC
			Amount:        strconv.FormatInt(amountSat, 10),
			Type:          constant.TxnTypeTransfer,
			TxFee:         txFee,
			Timestamp:     ts,
			Confirmations: confirmations,
			Status:        status,
		}

		transfers = append(transfers, transfer)
	}

	return transfers
}

// getFirstInputAddress returns the address of the first input with prevout data
func (b *BitcoinIndexer) getFirstInputAddress(tx *bitcoin.Transaction) string {
	for _, vin := range tx.Vin {
		if addr := bitcoin.GetInputAddress(&vin); addr != "" {
			return addr
		}
	}
	return ""
}

// calculateConfirmations calculates the number of confirmations for a transaction
func (b *BitcoinIndexer) calculateConfirmations(blockNumber, latestBlock uint64) uint64 {
	if blockNumber == 0 {
		return 0 // Mempool transaction
	}
	if latestBlock >= blockNumber {
		return latestBlock - blockNumber + 1
	}
	return 0
}

// extractTransfers is kept for backward compatibility with mempool worker
// Deprecated: Use extractTransfersFromTx instead
func (b *BitcoinIndexer) extractTransfers(
	tx *bitcoin.Transaction,
	blockNumber, ts, latestBlock uint64,
) []types.Transaction {
	return b.extractTransfersFromTx(tx, blockNumber, ts, latestBlock)
}

func (b *BitcoinIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := b.GetLatestBlockNumber(ctx)
	return err == nil
}

// GetConfirmedHeight returns the latest block height minus confirmations
// This ensures we only index blocks with sufficient confirmations to avoid reorgs
func (b *BitcoinIndexer) GetConfirmedHeight(ctx context.Context) (uint64, error) {
	latest, err := b.GetLatestBlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest block: %w", err)
	}

	if latest < b.confirmations {
		return 0, nil
	}

	return latest - b.confirmations, nil
}

// GetMempoolTransactions fetches and processes transactions from the mempool
// Returns transactions involving monitored addresses with 0 confirmations
func (b *BitcoinIndexer) GetMempoolTransactions(ctx context.Context) ([]types.Transaction, error) {
	// Get Bitcoin client from failover
	provider, err := b.failover.GetBestProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get bitcoin provider: %w", err)
	}

	btcClient, ok := provider.Client.(*bitcoin.BitcoinClient)
	if !ok {
		return nil, fmt.Errorf("invalid client type")
	}

	// Get latest block height for context
	latestBlock, err := b.GetLatestBlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block: %w", err)
	}

	// Get mempool transaction IDs
	result, err := btcClient.GetRawMempool(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get mempool: %w", err)
	}

	txids, ok := result.([]string)
	if !ok {
		return nil, fmt.Errorf("unexpected mempool format")
	}

	// Process each transaction
	var allTransfers []types.Transaction
	currentTime := uint64(time.Now().Unix())

	for _, txid := range txids {
		// Fetch transaction with prevout data resolved
		// This is critical for detecting FROM addresses
		tx, err := btcClient.GetTransactionWithPrevouts(ctx, txid)
		if err != nil {
			// Skip transactions we can't fetch (might be confirmed already)
			continue
		}

		// Extract transfers (blockNumber=0 for mempool, confirmations will be 0)
		transfers := b.extractTransfersFromTx(tx, 0, currentTime, latestBlock)
		allTransfers = append(allTransfers, transfers...)
	}

	return allTransfers, nil
}
