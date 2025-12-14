package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/cardano"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const defaultCardanoTxFetchConcurrency = 4


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
		header, err = api.GetBlockHeaderByNumber(ctx, blockNumber)
		if err != nil {
			return err
		}
		txHashes, err = api.GetTransactionsByBlock(ctx, blockNumber)
		if err != nil {
			return err
		}
		concurrency := c.config.Throttle.Concurrency
		if concurrency <= 0 {
			concurrency = 4
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

	results := make([]BlockResult, 0, len(blockNums))

	// For Cardano, we fetch blocks sequentially as the API doesn't support batch operations
	for _, blockNum := range blockNums {
		var (
			header   *cardano.BlockResponse
			txHashes []string
			txs      []cardano.Transaction
		)
		err := c.failover.ExecuteWithRetry(ctx, func(api cardano.CardanoAPI) error {
			var err error
			header, err = api.GetBlockHeaderByNumber(ctx, blockNum)
			if err != nil {
				return err
			}
			txHashes, err = api.GetTransactionsByBlock(ctx, blockNum)
			if err != nil {
				return err
			}
			concurrency := c.config.Throttle.Concurrency
			if concurrency <= 0 {
				concurrency = 4
			}
			txs, err = api.FetchTransactionsParallel(ctx, txHashes, concurrency)
			return err
		})

		if err != nil {
			logger.Warn("failed to fetch block", "block", blockNum, "error", err)
			results = append(results, BlockResult{
				Number: blockNum,
				Error: &Error{
					ErrorType: ErrorTypeBlockNotFound,
					Message:   err.Error(),
				},
			})
			continue
		}

		block := &cardano.Block{
			Hash:       header.Hash,
			Height:     header.Height,
			Slot:       header.Slot,
			Time:       header.Time,
			ParentHash: header.ParentHash,
		}
		for i := range txs {
			block.Txs = append(block.Txs, txs[i])
		}

		typesBlock := c.convertBlock(block)
		results = append(results, BlockResult{
			Number: blockNum,
			Block:  typesBlock,
		})
	}

	return results, nil
}

// convertBlock converts a Cardano block to the common Block type
func (c *CardanoIndexer) convertBlock(block *cardano.Block) *types.Block {
	transactions := make([]types.Transaction, 0)

	for _, tx := range block.Txs {
		// Skip failed transactions (e.g., script validation failed)
		// valid when: no script (nil) OR smart contract executed successfully (true)
		if tx.ValidContract != nil && !*tx.ValidContract {
			continue
		}
		// Representative from address: first input if available
		fromAddr := ""
		if len(tx.Inputs) > 0 && tx.Inputs[0].Address != "" {
			fromAddr = tx.Inputs[0].Address
		}

		// Convert fee (lovelace -> ADA) and assign to the first transfer produced by this tx
		feeAda := decimal.NewFromInt(int64(tx.Fee)).Div(decimal.NewFromInt(1_000_000))
		feeAssigned := false

		for _, out := range tx.Outputs {
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

