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
	"github.com/fystack/multichain-indexer/pkg/common/utils"
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

// convertBlockWithPrevoutResolution converts a block and resolves prevout data for transactions
func (b *BitcoinIndexer) convertBlockWithPrevoutResolution(ctx context.Context, btcBlock *bitcoin.Block) (*types.Block, error) {
	var allTransfers []types.Transaction
	var allUTXOEvents []types.UTXOEvent

	latestBlock := btcBlock.Height
	if btcBlock.Confirmations > 0 {
		latestBlock = btcBlock.Height + btcBlock.Confirmations - 1
	}

	provider, _ := b.failover.GetBestProvider()
	var btcClient *bitcoin.BitcoinClient
	if provider != nil {
		btcClient, _ = provider.Client.(*bitcoin.BitcoinClient)
	}

	for i := range btcBlock.Tx {
		tx := &btcBlock.Tx[i]

		if tx.IsCoinbase() {
			continue
		}

		if btcClient != nil && len(tx.Vin) > 0 && tx.Vin[0].PrevOut == nil {
			if tx.Vin[0].TxID != "" {
				if resolved, err := btcClient.GetTransactionWithPrevouts(ctx, tx.TxID); err == nil {
					for j := range tx.Vin {
						if j < len(resolved.Vin) && resolved.Vin[j].PrevOut != nil {
							tx.Vin[j].PrevOut = resolved.Vin[j].PrevOut
						}
					}
				}
			}
		}

		transfers := b.extractTransfersFromTx(tx, btcBlock.Height, btcBlock.Time, latestBlock)
		allTransfers = append(allTransfers, transfers...)

		if b.config.IndexUTXO {
			utxoEvent := b.extractUTXOEvent(tx, btcBlock.Height, btcBlock.Hash, btcBlock.Time, latestBlock)
			if utxoEvent != nil {
				allUTXOEvents = append(allUTXOEvents, *utxoEvent)
			}
		}
	}

	block := &types.Block{
		Number:       btcBlock.Height,
		Hash:         btcBlock.Hash,
		ParentHash:   btcBlock.PreviousBlockHash,
		Timestamp:    btcBlock.Time,
		Transactions: allTransfers,
	}

	block.SetMetadata("utxo_events", allUTXOEvents)

	return block, nil
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

// extractTransfersFromTx extracts all transfers from a transaction.
func (b *BitcoinIndexer) extractTransfersFromTx(
	tx *bitcoin.Transaction,
	blockNumber, ts, latestBlock uint64,
) []types.Transaction {
	var transfers []types.Transaction

	// Skip coinbase transactions
	if tx.IsCoinbase() {
		return transfers
	}

	fee := tx.CalculateFee()

	confirmations := b.calculateConfirmations(blockNumber, latestBlock)
	status := utils.CalculateTransactionStatus(confirmations, b.confirmations)

	fromAddr := b.getFirstInputAddress(tx)

	feeAssigned := false
	for _, vout := range tx.Vout {
		toAddr := bitcoin.GetOutputAddress(&vout)
		if toAddr == "" {
			continue // Skip unspendable outputs (OP_RETURN, etc.)
		}

		if normalized, err := bitcoin.NormalizeBTCAddress(toAddr); err == nil {
			toAddr = normalized
		}

		// For Transfer events, respect index_change_output config
		// (This filters what goes to transfer.event.dispatch)
		if !b.config.IndexChangeOutput && fromAddr != "" && fromAddr == toAddr {
			continue
		}

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
			Type:          constant.TxTypeNativeTransfer,
			TxFee:         txFee,
			Timestamp:     ts,
			Confirmations: confirmations,
			Status:        status,
		}

		transfers = append(transfers, transfer)
	}

	return transfers
}

func (b *BitcoinIndexer) extractUTXOEvent(
	tx *bitcoin.Transaction,
	blockNumber uint64,
	blockHash string,
	timestamp, latestBlock uint64,
) *types.UTXOEvent {
	if tx.IsCoinbase() {
		return nil
	}

	var created []types.UTXO
	var spent []types.SpentUTXO

	// Extract ALL created UTXOs (vouts) without filtering
	// Filtering happens at emission level based on monitored addresses
	for i, vout := range tx.Vout {
		addr := bitcoin.GetOutputAddress(&vout)
		if addr == "" {
			continue
		}

		if normalized, err := bitcoin.NormalizeBTCAddress(addr); err == nil {
			addr = normalized
		}

		amountSat := int64(vout.Value * 1e8)

		created = append(created, types.UTXO{
			TxHash:       tx.TxID,
			Vout:         uint32(i),
			Address:      addr,
			Amount:       strconv.FormatInt(amountSat, 10),
			ScriptPubKey: vout.ScriptPubKey.Hex,
		})
	}

	// Extract ALL spent UTXOs (vins) without filtering
	// Filtering happens at emission level based on monitored addresses
	for i, vin := range tx.Vin {
		if vin.PrevOut == nil {
			continue
		}

		addr := bitcoin.GetInputAddress(&vin)
		if addr == "" {
			continue
		}

		if normalized, err := bitcoin.NormalizeBTCAddress(addr); err == nil {
			addr = normalized
		}

		amountSat := int64(vin.PrevOut.Value * 1e8)

		spent = append(spent, types.SpentUTXO{
			TxHash:  vin.TxID,
			Vout:    vin.Vout,
			Vin:     uint32(i),
			Address: addr,
			Amount:  strconv.FormatInt(amountSat, 10),
		})
	}

	// Return event even if no monitored addresses
	// Filtering happens at emission level
	if len(created) == 0 && len(spent) == 0 {
		return nil
	}

	confirmations := b.calculateConfirmations(blockNumber, latestBlock)
	status := utils.CalculateTransactionStatus(confirmations, b.confirmations)
	fee := tx.CalculateFee()

	return &types.UTXOEvent{
		TxHash:        tx.TxID,
		NetworkId:     b.config.NetworkId,
		BlockNumber:   blockNumber,
		BlockHash:     blockHash,
		Timestamp:     timestamp,
		Created:       created,
		Spent:         spent,
		TxFee:         fee.String(),
		Status:        status,
		Confirmations: confirmations,
	}
}

func (b *BitcoinIndexer) getFirstInputAddress(tx *bitcoin.Transaction) string {
	for _, vin := range tx.Vin {
		if addr := bitcoin.GetInputAddress(&vin); addr != "" {
			if normalized, err := bitcoin.NormalizeBTCAddress(addr); err == nil {
				return normalized
			}
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
// Returns transactions and UTXO events involving monitored addresses with 0 confirmations
func (b *BitcoinIndexer) GetMempoolTransactions(ctx context.Context) ([]types.Transaction, []types.UTXOEvent, error) {
	provider, err := b.failover.GetBestProvider()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get bitcoin provider: %w", err)
	}

	btcClient, ok := provider.Client.(*bitcoin.BitcoinClient)
	if !ok {
		return nil, nil, fmt.Errorf("invalid client type")
	}

	latestBlock, err := b.GetLatestBlockNumber(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get latest block: %w", err)
	}

	result, err := btcClient.GetRawMempool(ctx, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get mempool: %w", err)
	}

	txids, ok := result.([]string)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected mempool format")
	}

	var allTransfers []types.Transaction
	var allUTXOEvents []types.UTXOEvent
	currentTime := uint64(time.Now().Unix())

	for _, txid := range txids {
		tx, err := btcClient.GetTransactionWithPrevouts(ctx, txid)
		if err != nil {
			continue
		}

		transfers := b.extractTransfersFromTx(tx, 0, currentTime, latestBlock)
		allTransfers = append(allTransfers, transfers...)

		if b.config.IndexUTXO {
			utxoEvent := b.extractUTXOEvent(tx, 0, "", currentTime, latestBlock)
			if utxoEvent != nil {
				allUTXOEvents = append(allUTXOEvents, *utxoEvent)
			}
		}
	}

	return allTransfers, allUTXOEvents, nil
}
