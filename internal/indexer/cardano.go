package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/cardano"
	"github.com/fystack/multichain-indexer/pkg/common/config"
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

// GetBlock fetches a single block
func (c *CardanoIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	var block *cardano.Block
	err := c.failover.ExecuteWithRetry(ctx, func(api cardano.CardanoAPI) error {
		b, err := api.GetBlockByNumber(ctx, blockNumber)
		block = b
		return err
	})
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block not found")
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
		var block *cardano.Block
		err := c.failover.ExecuteWithRetry(ctx, func(api cardano.CardanoAPI) error {
			b, err := api.GetBlockByNumber(ctx, blockNum)
			block = b
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

		if block == nil {
			results = append(results, BlockResult{
				Number: blockNum,
				Error: &Error{
					ErrorType: ErrorTypeBlockNil,
					Message:   "block is nil",
				},
			})
			continue
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

	// Process each transaction in the block
	for _, tx := range block.Txs {
		// Create transactions for each output (UTXO model)
		for _, output := range tx.Outputs {
			// Try to find the corresponding input to get the sender
			fromAddress := ""
			if len(tx.Inputs) > 0 {
				fromAddress = tx.Inputs[0].Address
			}

			txFee := decimal.NewFromInt(int64(tx.Fee))

			transaction := types.Transaction{
				TxHash:       tx.Hash,
				NetworkId:    c.GetNetworkId(),
				BlockNumber:  block.Height,
				FromAddress:  fromAddress,
				ToAddress:    output.Address,
				AssetAddress: "", // Cardano native asset, empty for ADA
				Amount:       fmt.Sprintf("%d", output.Amount),
				Type:         "transfer",
				TxFee:        txFee,
				Timestamp:    block.Time,
			}

			transactions = append(transactions, transaction)
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

