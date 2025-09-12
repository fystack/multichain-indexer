package evm

import (
	"math/big"
	"strings"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
)

func (t *Txn) NeedReceipt() bool {
	inputLen := len(strings.TrimSpace(t.Input))
	if inputLen <= 2 {
		return false
	}
	if inputLen >= 10 {
		sig := t.Input[:10]
		if sig == ERC20_TRANSFER_SIG || sig == ERC20_TRANSFER_FROM_SIG {
			return false
		}
	}
	return true
}

// calcFee computes the transaction fee from receipt if available, otherwise fallback to Txn Gas*GasPrice.
func (tx Txn) calcFee(receipt *TxnReceipt) decimal.Decimal {
	if receipt != nil {
		if gasUsed, err1 := utils.ParseHexBigInt(receipt.GasUsed); err1 == nil {
			if gasPrice, err2 := utils.ParseHexBigInt(receipt.EffectiveGasPrice); err2 == nil {
				return decimal.NewFromBigInt(new(big.Int).Mul(gasUsed, gasPrice), 0)
			}
		}
	}
	if gas, err1 := utils.ParseHexBigInt(tx.Gas); err1 == nil {
		if gasPrice, err2 := utils.ParseHexBigInt(tx.GasPrice); err2 == nil {
			return decimal.NewFromBigInt(new(big.Int).Mul(gas, gasPrice), 0)
		}
	}
	return decimal.Zero
}

// parseERC20Input decodes ERC20 transfer / transferFrom from tx.Input.
func (tx Txn) parseERC20Input(
	fee decimal.Decimal,
	network string,
	blockNumber, ts uint64,
) *types.Transaction {
	if len(tx.Input) < 10 {
		return nil
	}

	sig := tx.Input[:10]
	switch sig {
	case ERC20_TRANSFER_SIG:
		if len(tx.Input) < 138 {
			return nil
		}
		to := "0x" + tx.Input[10:74][24:]
		amount, err := utils.ParseHexBigInt("0x" + tx.Input[74:138])
		if err != nil {
			return nil
		}
		return &types.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    network,
			BlockNumber:  blockNumber,
			FromAddress:  tx.From,
			ToAddress:    to,
			AssetAddress: tx.To,
			Amount:       amount.String(),
			Type:         constant.TxnTypeERC20Transfer,
			TxFee:        fee,
			Timestamp:    ts,
		}

	case ERC20_TRANSFER_FROM_SIG:
		if len(tx.Input) < 202 {
			return nil
		}
		from := "0x" + tx.Input[10:74][24:]
		to := "0x" + tx.Input[74:138][24:]
		amount, err := utils.ParseHexBigInt("0x" + tx.Input[138:202])
		if err != nil {
			return nil
		}
		return &types.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    network,
			BlockNumber:  blockNumber,
			FromAddress:  from,
			ToAddress:    to,
			AssetAddress: tx.To,
			Amount:       amount.String(),
			Type:         constant.TxnTypeERC20Transfer,
			TxFee:        decimal.Zero,
			Timestamp:    ts,
		}
	}
	return nil
}

func (tx Txn) parseERC20Logs(
	network string,
	txHash string,
	logs []Log,
	blockNumber, ts uint64,
) []types.Transaction {
	var transfers []types.Transaction
	for _, log := range logs {
		parsed, err := log.parseERC20Transfers(txHash, network, blockNumber, ts)
		if err != nil {
			continue
		}
		transfers = append(transfers, parsed...)
	}
	return transfers
}

func (tx Txn) ExtractTransfers(
	network string,
	receipt *TxnReceipt,
	blockNumber, ts uint64,
) []types.Transaction {
	var out []types.Transaction
	fee := tx.calcFee(receipt)

	// native transfer
	if val, _ := utils.ParseHexBigInt(tx.Value); val.Sign() > 0 && tx.To != "" {
		out = append(out, types.Transaction{
			TxHash:      tx.Hash,
			NetworkId:   network,
			BlockNumber: blockNumber,
			FromAddress: tx.From,
			ToAddress:   tx.To,
			Amount:      val.String(),
			Type:        constant.TxnTypeTransfer,
			TxFee:       fee,
			Timestamp:   ts,
		})
	}
	// ERC20
	if receipt != nil {
		out = append(out, tx.parseERC20Logs(network, tx.Hash, receipt.Logs, blockNumber, ts)...)
	} else if erc20 := tx.parseERC20Input(fee, network, blockNumber, ts); erc20 != nil {
		out = append(out, *erc20)
	}
	return utils.DedupTransfers(out)
}
