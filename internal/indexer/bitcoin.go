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
		// Verbosity 2 = full transaction details with prevout data
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

	return b.convertBlock(btcBlock)
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

func (b *BitcoinIndexer) convertBlock(btcBlock *bitcoin.Block) (*types.Block, error) {
	var allTransfers []types.Transaction

	for _, tx := range btcBlock.Tx {
		// Extract transfers for monitored addresses
		// This handles both incoming (TO) and outgoing (FROM) transfers
		transfers := b.extractTransfers(&tx, btcBlock.Height, btcBlock.Time)
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

// extractTransfers extracts relevant transfers for monitored addresses
// This handles both incoming (TO monitored addresses) and outgoing (FROM monitored addresses) transfers
func (b *BitcoinIndexer) extractTransfers(
	tx *bitcoin.Transaction,
	blockNumber, ts uint64,
) []types.Transaction {
	var transfers []types.Transaction

	// Skip coinbase transactions
	if tx.IsCoinbase() {
		return transfers
	}

	// Calculate fee once
	fee := tx.CalculateFee()

	// Track monitored addresses in this transaction
	monitoredInputs := make(map[string]bool)  // Addresses sending (FROM)
	monitoredOutputs := make(map[string]bool) // Addresses receiving (TO)

	// PASS 1: Identify monitored inputs (outgoing/spending)
	for _, vin := range tx.Vin {
		if vin.PrevOut == nil {
			continue
		}

		inputAddr := vin.PrevOut.ScriptPubKey.Address
		if inputAddr == "" && len(vin.PrevOut.ScriptPubKey.Addresses) > 0 {
			inputAddr = vin.PrevOut.ScriptPubKey.Addresses[0]
		}

		if inputAddr != "" && b.pubkeyStore != nil && b.pubkeyStore.Exist(enum.NetworkTypeBtc, inputAddr) {
			monitoredInputs[inputAddr] = true
		}
	}

	// PASS 2: Identify monitored outputs (incoming/receiving)
	for _, vout := range tx.Vout {
		outputAddr := vout.ScriptPubKey.Address
		if outputAddr == "" && len(vout.ScriptPubKey.Addresses) > 0 {
			outputAddr = vout.ScriptPubKey.Addresses[0]
		}

		if outputAddr != "" && b.pubkeyStore != nil && b.pubkeyStore.Exist(enum.NetworkTypeBtc, outputAddr) {
			monitoredOutputs[outputAddr] = true
		}
	}

	// PASS 3: Create transfer records for monitored addresses

	// 3A: Incoming transfers (TO monitored addresses)
	for _, vout := range tx.Vout {
		toAddr := vout.ScriptPubKey.Address
		if toAddr == "" && len(vout.ScriptPubKey.Addresses) > 0 {
			toAddr = vout.ScriptPubKey.Addresses[0]
		}

		if toAddr == "" || !monitoredOutputs[toAddr] {
			continue
		}

		// Get "from" address (first input address)
		fromAddr := ""
		if len(tx.Vin) > 0 && tx.Vin[0].PrevOut != nil {
			fromAddr = tx.Vin[0].PrevOut.ScriptPubKey.Address
			if fromAddr == "" && len(tx.Vin[0].PrevOut.ScriptPubKey.Addresses) > 0 {
				fromAddr = tx.Vin[0].PrevOut.ScriptPubKey.Addresses[0]
			}
		}

		// Convert BTC to satoshis for precision (multiply by 1e8)
		amountSat := int64(vout.Value * 1e8)

		transfer := types.Transaction{
			TxHash:       tx.TxID,
			NetworkId:    b.config.NetworkId,
			BlockNumber:  blockNumber,
			FromAddress:  fromAddr,
			ToAddress:    toAddr,
			AssetAddress: "", // Empty for native BTC
			Amount:       strconv.FormatInt(amountSat, 10),
			Type:         constant.TxnTypeTransfer,
			TxFee:        fee,
			Timestamp:    ts,
		}

		transfers = append(transfers, transfer)
	}

	// 3B: Outgoing transfers (FROM monitored addresses)
	for fromAddr := range monitoredInputs {
		// Find outputs NOT going to same monitored address (actual spends)
		for _, vout := range tx.Vout {
			toAddr := vout.ScriptPubKey.Address
			if toAddr == "" && len(vout.ScriptPubKey.Addresses) > 0 {
				toAddr = vout.ScriptPubKey.Addresses[0]
			}

			if toAddr == "" {
				continue // Skip unspendable outputs
			}

			// Skip if this output goes back to a monitored address (change)
			// and we already recorded it in the incoming section
			if monitoredOutputs[toAddr] {
				continue
			}

			// This is a true outgoing transfer
			amountSat := int64(vout.Value * 1e8)

			transfer := types.Transaction{
				TxHash:       tx.TxID,
				NetworkId:    b.config.NetworkId,
				BlockNumber:  blockNumber,
				FromAddress:  fromAddr,
				ToAddress:    toAddr,
				AssetAddress: "",
				Amount:       strconv.FormatInt(amountSat, 10),
				Type:         constant.TxnTypeTransfer,
				TxFee:        decimal.Zero, // Fee already assigned to incoming transfer
				Timestamp:    ts,
			}

			transfers = append(transfers, transfer)
		}
	}

	return transfers
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
