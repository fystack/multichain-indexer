package evm

import (
	"math/big"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
)

func (t *Txn) NeedReceipt() bool {
	inputLen := len(strings.TrimSpace(t.Input))
	if inputLen <= 2 {
		return true
	}
	if inputLen >= 10 {
		sig := t.Input[:10]
		if sig == ERC20_TRANSFER_SIG || sig == ERC20_TRANSFER_FROM_SIG {
			return true
		}
	}
	return false
}

const WEI_PER_ETH = 1e18

// calcFee computes the transaction fee from receipt if available, otherwise fallback to Txn Gas*GasPrice.
// Returns the fee in ETH (divided by 1e18 from Wei).
func (tx Txn) calcFee(receipt *TxnReceipt) decimal.Decimal {
	if receipt != nil {
		if gasUsed, err1 := utils.ParseHexBigInt(receipt.GasUsed); err1 == nil {
			if gasPrice, err2 := utils.ParseHexBigInt(receipt.EffectiveGasPrice); err2 == nil {
				weiAmount := new(big.Int).Mul(gasUsed, gasPrice)
				result := decimal.NewFromBigInt(weiAmount, 0).Div(decimal.NewFromInt(WEI_PER_ETH))
				return result
			}
		}
	}
	if gas, err1 := utils.ParseHexBigInt(tx.Gas); err1 == nil {
		if gasPrice, err2 := utils.ParseHexBigInt(tx.GasPrice); err2 == nil {
			weiAmount := new(big.Int).Mul(gas, gasPrice)
			return decimal.NewFromBigInt(weiAmount, 0).Div(decimal.NewFromInt(WEI_PER_ETH))
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
	// 10 = 2+8
	//"0x" prefix = 2 characters,
	// selector = 8 characters.
	if len(tx.Input) < 10 {
		return nil
	}

	sig := tx.Input[:10]
	switch sig {
	case ERC20_TRANSFER_SIG:
		to, amount, err := DecodeERC20TransferInput(tx.Input)
		if err != nil {
			return nil
		}
		return &types.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    network,
			BlockNumber:  blockNumber,
			FromAddress:  ToChecksumAddress(tx.From),
			ToAddress:    to,
			AssetAddress: ToChecksumAddress(tx.To),
			Amount:       amount.String(),
			Type:         constant.TxTypeTokenTransfer,
			TxFee:        fee,
			Timestamp:    ts,
		}

	case ERC20_TRANSFER_FROM_SIG:
		from, to, amount, err := DecodeERC20TransferFromInput(tx.Input)
		if err != nil {
			return nil
		}
		return &types.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    network,
			BlockNumber:  blockNumber,
			FromAddress:  from,
			ToAddress:    to,
			AssetAddress: ToChecksumAddress(tx.To),
			Amount:       amount.String(),
			Type:         constant.TxTypeTokenTransfer,
			TxFee:        fee,
			Timestamp:    ts,
		}
	}
	return nil
}

func (tx Txn) parseERC20Logs(
	fee decimal.Decimal,
	network string,
	txHash string,
	logs []Log,
	blockNumber, ts uint64,
) []types.Transaction {
	var transfers []types.Transaction
	for _, log := range logs {
		parsed, err := log.parseERC20Transfers(fee, txHash, network, blockNumber, ts)
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
	if val, _ := utils.ParseHexBigInt(tx.Value); val.Sign() > 0 && tx.To != "" && strings.TrimPrefix(tx.Input, "0x") == "" {
		out = append(out, types.Transaction{
			TxHash:      tx.Hash,
			NetworkId:   network,
			BlockNumber: blockNumber,
			FromAddress: ToChecksumAddress(tx.From),
			ToAddress:   ToChecksumAddress(tx.To),
			Amount:      val.String(),
			Type:        constant.TxTypeNativeTransfer,
			TxFee:       fee,
			Timestamp:   ts,
		})
	}
	// ERC20
	if receipt != nil {
		out = append(out, tx.parseERC20Logs(fee, network, tx.Hash, receipt.Logs, blockNumber, ts)...)
	} else if erc20 := tx.parseERC20Input(fee, network, blockNumber, ts); erc20 != nil {
		out = append(out, *erc20)
	}
	return utils.DedupTransfers(out)
}
