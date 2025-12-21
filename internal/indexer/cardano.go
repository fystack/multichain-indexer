package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"
	"sync"


	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/cardano"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)


type CardanoIndexer struct {
	chainName string
	config    config.ChainConfig
	failover  *rpc.Failover[cardano.CardanoAPI]
}

func NewCardanoIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[cardano.CardanoAPI],
) *CardanoIndexer {
	return &CardanoIndexer{
		chainName: chainName,
		config:    cfg,
		failover:  failover,
	}
}

func (c *CardanoIndexer) GetName() string                  { return strings.ToUpper(c.chainName) }
func (c *CardanoIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeCardano }
func (c *CardanoIndexer) GetNetworkInternalCode() string {
	return c.config.InternalCode
}
func (c *CardanoIndexer) GetNetworkId() string {
	return c.config.NetworkId
}

// GetLatestBlockNumber fetches the latest block number
func (c *CardanoIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := c.failover.ExecuteWithRetry(ctx, func(api cardano.CardanoAPI) error {
		n, err := api.GetLatestBlockNumber(ctx)
		latest = n
		return err
	})
	return latest, err
}

// GetBlock fetches a single block (header + txs fetched in parallel with quota)
func (c *CardanoIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	var (
		header   *cardano.BlockResponse
		txHashes []string
		txs      []cardano.Transaction
	)

	err := c.failover.ExecuteWithRetry(ctx, func(api cardano.CardanoAPI) error {
		var err error
		// Fetch block header first
		header, err = api.GetBlockHeaderByNumber(ctx, blockNumber)
		if err != nil {
			return err
		}
		// Use block hash to fetch transactions (avoids duplicate GetBlockHeaderByNumber call)
		txHashes, err = api.GetTransactionsByBlockHash(ctx, header.Hash)
		if err != nil {
			return err
		}
		concurrency := c.config.Throttle.Concurrency
		if concurrency <= 0 {
			concurrency = cardano.DefaultTxFetchConcurrency
		}
		// Clamp concurrency to the number of transactions to avoid creating useless goroutines
		if numTxs := len(txHashes); numTxs > 0 && numTxs < concurrency {
			concurrency = numTxs
		}
		txs, err = api.FetchTransactionsParallel(ctx, txHashes, concurrency)
		return err
	})
	if err != nil {
		return nil, err
	}

	block := &cardano.Block{
		Hash:       header.Hash,
		Height:     header.Height,
		Slot:       header.Slot,
		Time:       header.Time,
		ParentHash: header.ParentHash,
	}
	// attach txs
	for i := range txs {
		block.Txs = append(block.Txs, txs[i])
	}

	return c.convertBlock(block), nil
}

// GetBlocks fetches a range of blocks
func (c *CardanoIndexer) GetBlocks(
	ctx context.Context,
	from, to uint64,
	isParallel bool,
) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: from=%d, to=%d", from, to)
	}

	blockNums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		blockNums = append(blockNums, n)
	}

	return c.fetchBlocks(ctx, blockNums, isParallel)
}

// GetBlocksByNumbers fetches blocks by their numbers
func (c *CardanoIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	return c.fetchBlocks(ctx, blockNumbers, false)
}

// fetchBlocks is the internal method to fetch blocks
func (c *CardanoIndexer) fetchBlocks(
	ctx context.Context,
	blockNums []uint64,
	isParallel bool,
) ([]BlockResult, error) {
	if len(blockNums) == 0 {
		return nil, nil
	}

	// For Cardano, we should fetch blocks sequentially to avoid rate limiting
	// because each block fetch involves multiple API calls (header + txs + utxos for each tx)
	// With Blockfrost free tier (10 RPS), parallel block fetching can easily exceed limits
	workers := 1 // Always use 1 worker for block fetching to be safe

	// Only use configured concurrency if explicitly parallel and concurrency > 1
	if isParallel && c.config.Throttle.Concurrency > 1 {
		workers = c.config.Throttle.Concurrency
		if workers > len(blockNums) {
			workers = len(blockNums)
		}
	}

	type job struct {
		num   uint64
		index int
	}

	jobs := make(chan job, len(blockNums))
	results := make([]BlockResult, len(blockNums))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			blockCount := 0
			for j := range jobs {
				// Early exit if context is canceled
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Add delay every 5 blocks to avoid rate limiting
				// This is critical for Cardano/Blockfrost to prevent burst traffic
				if blockCount > 0 && blockCount%5 == 0 {
					logger.Debug("Rate limit protection: pausing between blocks",
						"worker", workerID, "blocks_processed", blockCount)
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}

				block, err := c.GetBlock(ctx, j.num)
				if err != nil {
					logger.Warn("failed to fetch block", "block", j.num, "error", err)
					results[j.index] = BlockResult{
						Number: j.num,
						Error:  &Error{ErrorType: ErrorTypeBlockNotFound, Message: err.Error()},
					}
				} else {
					results[j.index] = BlockResult{Number: j.num, Block: block}
				}
				blockCount++
			}
		}(i)
	}

	// Feed jobs to workers and close channel when done
	go func() {
		defer close(jobs)
		for i, num := range blockNums {
			select {
			case jobs <- job{num: num, index: i}:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()

	// Check if the context was canceled during the operation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return results, nil
}

// convertBlock converts a Cardano block to the common Block type
func (c *CardanoIndexer) convertBlock(block *cardano.Block) *types.Block {
	// Pre-allocate slice with a reasonable capacity to reduce re-allocations
	estimatedSize := len(block.Txs) * 2
	transactions := make([]types.Transaction, 0, estimatedSize)

	for _, tx := range block.Txs {
		// Skip failed transactions (e.g., script validation failed)
		// valid when: no script (nil) OR smart contract executed successfully (true)
		if tx.ValidContract != nil && !*tx.ValidContract {
			continue
		}
		// Find a representative from address from non-reference, non-collateral inputs
		fromAddr := ""
		for _, inp := range tx.Inputs {
			if !inp.Reference && !inp.Collateral && inp.Address != "" {
				fromAddr = inp.Address
				break
			}
		}

		// Convert fee (lovelace -> ADA) and assign to the first transfer produced by this tx
		feeAda := decimal.NewFromInt(int64(tx.Fee)).Div(decimal.NewFromInt(1_000_000))
		feeAssigned := false

		for _, out := range tx.Outputs {
			// Skip collateral outputs as they are not considered transfers to the recipient
			if out.Collateral {
				continue
			}
			for _, amt := range out.Amounts {
				if amt.Quantity == "" || amt.Quantity == "0" {
					continue
				}
				tr := types.Transaction{
					TxHash:      tx.Hash,
					NetworkId:   c.GetNetworkId(),
					BlockNumber: block.Height,
					FromAddress: fromAddr,
					ToAddress:   out.Address,
					Amount:      amt.Quantity,
					Type:        constant.TxnTypeTransfer,
					Timestamp:   block.Time,
				}
				if amt.Unit != "lovelace" {
					tr.AssetAddress = amt.Unit
				}
				if !feeAssigned {
					tr.TxFee = feeAda
					feeAssigned = true
				}
				transactions = append(transactions, tr)
			}
		}
	}

	return &types.Block{
		Number:       block.Height,
		Hash:         block.Hash,
		ParentHash:   block.ParentHash,
		Timestamp:    block.Time,
		Transactions: transactions,
	}
}

// IsHealthy checks if the indexer is healthy
func (c *CardanoIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.GetLatestBlockNumber(ctx)
	return err == nil
}