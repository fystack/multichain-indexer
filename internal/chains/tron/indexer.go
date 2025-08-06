package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/fystack/transaction-indexer/internal/chains"
	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/ratelimiter"
	"github.com/fystack/transaction-indexer/internal/types"
	"github.com/shopspring/decimal"
)

type Indexer struct {
	client *TronClient
	name   string
	config config.ChainConfig
}

func NewIndexerWithConfig(nodes []string, cfg config.ChainConfig) *Indexer {
	clientCfg := TronClientConfig{
		RequestTimeout: cfg.Client.RequestTimeout,
		RateLimit: RateLimitConfig{
			RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
			BurstSize:         cfg.RateLimit.BurstSize,
		},
		MaxRetries: cfg.Client.MaxRetries,
		RetryDelay: cfg.Client.RetryDelay,
	}

	return &Indexer{
		client: NewTronClient(nodes, clientCfg),
		name:   chains.ChainTron,
		config: cfg,
	}
}

func (i *Indexer) GetName() string {
	return i.name
}

func (i *Indexer) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	result, err := i.client.GetNowBlock(ctx)
	if err != nil {
		return 0, err
	}
	var blockResponse simpleBlockRaw
	if err := json.Unmarshal(result, &blockResponse); err != nil {
		return 0, fmt.Errorf("failed to decode block response: %w", err)
	}

	return blockResponse.BlockHeader.RawData.Number, nil
}

func (i *Indexer) GetBlock(ctx context.Context, number int64) (*types.Block, error) {
	result, err := i.client.GetBlockByNum(ctx, number)
	if err != nil {
		return nil, err
	}
	return i.parseBlock(result)
}

func (i *Indexer) GetBlocks(ctx context.Context, from, to int64) ([]chains.BlockResult, error) {
	var results []chains.BlockResult

	for blockNum := from; blockNum <= to; blockNum++ {
		block, err := i.GetBlock(ctx, blockNum)
		result := chains.BlockResult{Number: blockNum}

		if err != nil {
			result.Error = &chains.Error{
				ErrorType: chains.ErrorTypeBlockNotFound,
				Message:   fmt.Sprintf("failed to get block: %v", err),
			}
		} else if block == nil {
			result.Error = &chains.Error{
				ErrorType: chains.ErrorTypeBlockNil,
				Message:   "block is nil",
			}
		} else {
			result.Block = block
		}
		results = append(results, result)
	}
	return results, nil
}

func (i *Indexer) IsHealthy() bool {
	_, err := i.GetLatestBlockNumber(context.Background())
	return err == nil
}

func (i *Indexer) GetRateLimitStats() map[string]ratelimiter.Stats {
	return i.client.RateLimiter.GetStats()
}

func (i *Indexer) Close() {
	i.client.Close()
}

func (i *Indexer) parseBlock(data json.RawMessage) (*types.Block, error) {
	var raw tronBlock
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid block data: %w", err)
	}

	number := uint64(raw.BlockHeader.RawData.Number)
	timestamp := uint64(raw.BlockHeader.RawData.Timestamp)

	var txs []types.Transaction
	for _, rawTx := range raw.Transactions {
		// Parse all types of transfers
		transfers := i.parseTransfers(rawTx, number, timestamp)
		txs = append(txs, transfers...)
	}

	return &types.Block{
		Number:       number,
		Hash:         raw.BlockID,
		ParentHash:   "", // Tron doesn't provide parent hash in this format
		Timestamp:    timestamp,
		Transactions: txs,
	}, nil
}

func (i *Indexer) parseTransfers(rawTx tronTransaction, blockNumber, timestamp uint64) []types.Transaction {
	var transfers []types.Transaction

	for _, contract := range rawTx.RawData.Contract {
		switch contract.Type {
		case TransferContractType:
			// Native TRX transfer
			if transfer := i.parseNativeTransfer(&contract, rawTx.TxID, blockNumber, timestamp); transfer != nil {
				transfers = append(transfers, *transfer)
			}

		case TransferAssetContractType:
			// TRC-10 token transfer
			if transfer := i.parseTRC10Transfer(&contract, rawTx.TxID, blockNumber, timestamp); transfer != nil {
				transfers = append(transfers, *transfer)
			}

		case TriggerSmartContractType:
			// TRC-20 token transfer (and other smart contract calls)
			if trc20Transfers := i.parseTRC20Transfers(&contract, rawTx.TxID, blockNumber, timestamp); len(trc20Transfers) > 0 {
				transfers = append(transfers, trc20Transfers...)
			}
		}
	}

	return transfers
}

func (i *Indexer) parseNativeTransfer(contract *Contract, txID string, blockNumber, timestamp uint64) *types.Transaction {
	// Parse native TRX transfer
	value, ok := new(big.Int).SetString(contract.Parameter.Value.Amount.String(), 10)
	if !ok || value.Cmp(big.NewInt(0)) <= 0 {
		return nil
	}

	return &types.Transaction{
		TxHash:       txID,
		NetworkId:    i.GetName(),
		BlockNumber:  blockNumber,
		FromAddress:  strings.ToLower(contract.Parameter.Value.OwnerAddress),
		ToAddress:    strings.ToLower(contract.Parameter.Value.ToAddress),
		AssetAddress: "0", // TRX native token
		Amount:       value.String(),
		Type:         TxnNative.String(),
		TxFee:        decimal.NewFromInt(0), // Default to 0
		Timestamp:    timestamp,
	}
}

func (i *Indexer) parseTRC10Transfer(contract *Contract, txID string, blockNumber, timestamp uint64) *types.Transaction {
	// Parse TRC-10 token transfer
	value, ok := new(big.Int).SetString(contract.Parameter.Value.Amount.String(), 10)
	if !ok || value.Cmp(big.NewInt(0)) <= 0 {
		return nil
	}

	return &types.Transaction{
		TxHash:       txID,
		NetworkId:    i.GetName(),
		BlockNumber:  blockNumber,
		FromAddress:  strings.ToLower(contract.Parameter.Value.OwnerAddress),
		ToAddress:    strings.ToLower(contract.Parameter.Value.ToAddress),
		AssetAddress: contract.Parameter.Value.AssetName, // TRC-10 asset name
		Amount:       value.String(),
		Type:         TxnTRC10.String(),
		TxFee:        decimal.NewFromInt(0), // Default to 0
		Timestamp:    timestamp,
	}
}

func (i *Indexer) parseTRC20Transfers(contract *Contract, txID string, blockNumber, timestamp uint64) []types.Transaction {
	var transfers []types.Transaction

	// Check if this is a transfer call
	if len(contract.Parameter.Value.Data) > 0 {
		// Decode TRC-20 transfer data
		transferData := decodeTRC20TransferData(contract.Parameter.Value.Data)
		if transferData != nil {
			transfers = append(transfers, types.Transaction{
				TxHash:       txID,
				NetworkId:    i.GetName(),
				BlockNumber:  blockNumber,
				FromAddress:  strings.ToLower(contract.Parameter.Value.OwnerAddress),
				ToAddress:    strings.ToLower(transferData.ToAddress),
				AssetAddress: strings.ToLower(contract.Parameter.Value.ContractAddress), // TRC-20 contract address
				Amount:       transferData.Amount.String(),
				Type:         TxnTRC20.String(),
				TxFee:        decimal.NewFromInt(0), // Default to 0
				Timestamp:    timestamp,
			})
		}
	}

	return transfers
}
