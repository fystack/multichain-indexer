package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/internal/rpc"
	"github.com/fystack/transaction-indexer/internal/rpc/tron"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

type TronIndexer struct {
	chainName string
	config    config.ChainConfig
	failover  *rpc.Failover[tron.TronAPI]
}

func NewTronIndexer(chainName string, cfg config.ChainConfig, f *rpc.Failover[tron.TronAPI]) *TronIndexer {
	return &TronIndexer{
		chainName: chainName,
		config:    cfg,
		failover:  f,
	}
}

func (t *TronIndexer) GetName() string                  { return strings.ToUpper(t.chainName) }
func (t *TronIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeTron }
func (t *TronIndexer) GetNetworkInternalCode() string {
	return t.config.InternalCode
}
func (t *TronIndexer) GetNetworkId() string {
	return t.config.NetworkId
}

func (t *TronIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := t.failover.ExecuteWithRetry(ctx, func(c tron.TronAPI) error {
		n, err := c.GetBlockNumber(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (t *TronIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	var (
		wg        sync.WaitGroup
		tronBlock *tron.Block
		txns      []*tron.TxnInfo
		blockErr  error
		txnErr    error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		blockErr = t.failover.ExecuteWithRetry(ctx, func(c tron.TronAPI) error {
			b, err := c.GetBlockByNumber(ctx, fmt.Sprintf("%d", blockNumber), true)
			if err == nil {
				tronBlock = b
			}
			return err
		})
	}()
	go func() {
		defer wg.Done()
		txnErr = t.failover.ExecuteWithRetry(ctx, func(c tron.TronAPI) error {
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

func (t *TronIndexer) processBlock(
	tronBlock *tron.Block,
	txns []*tron.TxnInfo,
) (*types.Block, error) {
	block := &types.Block{
		Number:       uint64(tronBlock.BlockHeader.RawData.Number),
		Hash:         tronBlock.BlockID,
		ParentHash:   tronBlock.BlockHeader.RawData.ParentHash,
		Timestamp:    tron.ConvertTronTimestamp(tronBlock.BlockHeader.RawData.Timestamp),
		Transactions: []types.Transaction{},
	}

	// Index TxnInfo by ID
	infoByID := make(map[string]*tron.TxnInfo, len(txns))
	for _, ti := range txns {
		if ti != nil && ti.ID != "" {
			infoByID[ti.ID] = ti
		}
	}

	feeAssigned := make(map[string]bool, len(txns))

	// Parse receipts logs (TRC20/721)
	for _, ti := range txns {
		if ti == nil {
			continue
		}
		transfers := []types.Transaction{}
		for _, log := range ti.Log {
			parsed, err := log.ParseTRC20Transfers(
				ti.ID,
				t.GetNetworkId(),
				uint64(ti.BlockNumber),
				tron.ConvertTronTimestamp(ti.BlockTimestamp),
			)
			if err == nil && len(parsed) > 0 {
				transfers = append(transfers, parsed...)
			}
		}
		if len(transfers) > 0 {
			// Assign fee to first transfer
			transfers[0].TxFee = ti.TotalFeeTRX()
			feeAssigned[ti.ID] = true
			block.Transactions = append(block.Transactions, transfers...)
		}
	}

	// Parse top-level contracts (TRX transfer, TRC10)
	for _, rawTx := range tronBlock.Transactions {
		if !rawTx.IsSuccessful() {
			logger.Debug("Skipping failed transaction", "txid", rawTx.TxID)
			continue
		}

		ts := tron.ConvertTronTimestamp(tronBlock.BlockHeader.RawData.Timestamp)
		blkNum := uint64(tronBlock.BlockHeader.RawData.Number)
		fee := decimal.Zero

		if ti := infoByID[rawTx.TxID]; ti != nil {
			ts = tron.ConvertTronTimestamp(ti.BlockTimestamp)
			blkNum = uint64(ti.BlockNumber)
			fee = ti.TotalFeeTRX()
		}

		for _, contract := range rawTx.RawData.Contract {
			var tr *types.Transaction
			var err error

			switch contract.Type {
			case tron.ContractTypeTransfer:
				var transfer tron.TransferContract
				if err = json.Unmarshal(contract.Parameter.Value, &transfer); err == nil {
					tr = &types.Transaction{
						TxHash:       rawTx.TxID,
						NetworkId:    t.GetNetworkId(),
						BlockNumber:  blkNum,
						FromAddress:  tron.HexToTronAddress(transfer.OwnerAddress),
						ToAddress:    tron.HexToTronAddress(transfer.ToAddress),
						AssetAddress: "",
						Amount:       decimal.NewFromInt(transfer.Amount).String(),
						Type:         constant.TxnTypeTransfer,
						Timestamp:    ts,
					}
				}
			case tron.ContractTypeTransferAsset:
				var asset tron.TransferAssetContract
				if err = json.Unmarshal(contract.Parameter.Value, &asset); err == nil {
					tr = &types.Transaction{
						TxHash:       rawTx.TxID,
						NetworkId:    t.GetNetworkId(),
						BlockNumber:  blkNum,
						FromAddress:  tron.HexToTronAddress(asset.OwnerAddress),
						ToAddress:    tron.HexToTronAddress(asset.ToAddress),
						AssetAddress: asset.AssetName,
						Amount:       decimal.NewFromInt(asset.Amount).String(),
						Type:         constant.TxnTypeTRC10Transfer,
						Timestamp:    ts,
					}
				}
			}
			if tr == nil {
				continue
			}

			// Assign fee only if not already assigned
			if !feeAssigned[rawTx.TxID] {
				tr.TxFee = fee
				feeAssigned[rawTx.TxID] = true
			}
			block.Transactions = append(block.Transactions, *tr)
		}
	}

	return block, nil
}

func (t *TronIndexer) GetBlocks(
	ctx context.Context,
	start, end uint64,
	isParallel bool,
) ([]BlockResult, error) {
	var nums []uint64
	for i := start; i <= end; i++ {
		nums = append(nums, i)
	}
	return t.GetBlocksByNumbers(ctx, nums)
}

func (t *TronIndexer) GetBlocksByNumbers(
	ctx context.Context,
	nums []uint64,
) ([]BlockResult, error) {
	return t.getBlocks(ctx, nums)
}

func (t *TronIndexer) getBlocks(ctx context.Context, nums []uint64) ([]BlockResult, error) {
	if len(nums) == 0 {
		return nil, nil
	}
	blocks := make([]BlockResult, len(nums))

	workers := len(nums)
	workers = min(workers, t.config.Throttle.Concurrency)

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
				blk, err := t.GetBlock(ctx, j.num)
				blocks[j.index] = BlockResult{Number: j.num, Block: blk}
				if err != nil {
					blocks[j.index].Error = &Error{
						ErrorType: ErrorTypeUnknown,
						Message:   err.Error(),
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i, num := range nums {
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
	for _, b := range blocks {
		if b.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", b.Number, b.Error.Message)
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
