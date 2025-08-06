package evm

import (
	"math/big"
	"strings"

	"github.com/fystack/transaction-indexer/internal/types"
	"github.com/shopspring/decimal"
)

// isHighProbabilityERC20Transfer checks if a transaction is likely to be an ERC-20 transfer
// This is more selective than the previous isPotentialERC20Transfer to reduce unnecessary API calls
func isHighProbabilityERC20Transfer(tx rawTx) bool {
	// Must have input data (not just native transfer)
	if tx.Input == "0x" || tx.Input == "" || len(tx.Input) < 10 {
		return false
	}

	// Must have a recipient (not contract creation)
	if tx.To == "" {
		return false
	}

	// Check for ERC-20 transfer function signature (transfer, transferFrom)
	// ERC-20 transfer function signature: 0xa9059cbb (transfer) or 0x23b872dd (transferFrom)
	input := strings.ToLower(tx.Input)
	if !strings.HasPrefix(input, "0xa9059cbb") && !strings.HasPrefix(input, "0x23b872dd") {
		return false
	}

	// Must have some value or gas usage to indicate it's not a failed transaction
	if tx.Value == "0x0" && len(tx.Input) < 74 { // Minimum length for ERC-20 transfer
		return false
	}

	return true
}

// parseNativeTransfer extracts native ETH transfers
func parseNativeTransfer(tx rawTx, blockNumber, timestamp uint64, networkId string) *types.Transaction {
	// Skip if no value transferred
	if tx.Value == "0x0" || tx.Value == "0x" || tx.Value == "" {
		return nil
	}

	// Parse the value
	value, ok := new(big.Int).SetString(strings.TrimPrefix(tx.Value, "0x"), 16)
	if !ok || value.Cmp(big.NewInt(0)) <= 0 {
		return nil
	}

	// Calculate transaction fee
	gasUsed, _ := types.ParseInt64(tx.Gas) // fallback to gas limit if gasUsed not available
	gasPrice, _ := types.ParseInt64(tx.GasPrice)
	txFee := decimal.NewFromInt(gasUsed * gasPrice).Div(decimal.NewFromInt(1e18)) // Convert to ETH

	return &types.Transaction{
		TxHash:       tx.Hash,
		NetworkId:    networkId,
		BlockNumber:  blockNumber,
		FromAddress:  strings.ToLower(tx.From),
		ToAddress:    strings.ToLower(tx.To),
		AssetAddress: NativeAssetAddress,
		Amount:       value.String(), // Amount in wei
		Type:         "native",
		TxFee:        txFee,
		Timestamp:    timestamp,
	}
}

// parseERC20TransfersFromReceipt extracts ERC-20 transfers from transaction receipt
func parseERC20TransfersFromReceipt(tx rawTx, receipt *rawReceipt, blockNumber, timestamp uint64, networkId string) []types.Transaction {
	var transfers []types.Transaction

	// Calculate transaction fee
	gasUsed, _ := types.ParseInt64(receipt.GasUsed)
	gasPrice, _ := types.ParseInt64(tx.GasPrice)
	txFee := decimal.NewFromInt(gasUsed * gasPrice).Div(decimal.NewFromInt(1e18))

	for _, log := range receipt.Logs {
		// Check if this log is a Transfer event
		if len(log.Topics) < 3 || log.Topics[0] != ERC20TransferSig {
			continue
		}

		// Extract from address (topic[1])
		fromAddr := "0x" + strings.TrimPrefix(log.Topics[1], "0x")[24:] // Last 20 bytes (40 hex chars)

		// Extract to address (topic[2])
		toAddr := "0x" + strings.TrimPrefix(log.Topics[2], "0x")[24:] // Last 20 bytes (40 hex chars)

		// Extract amount from data field
		data := strings.TrimPrefix(log.Data, "0x")
		if len(data) < 64 { // uint256 should be 64 hex chars
			continue
		}

		amount, ok := new(big.Int).SetString(data[:64], 16)
		if !ok {
			continue
		}

		transfers = append(transfers, types.Transaction{
			TxHash:       tx.Hash,
			NetworkId:    networkId,
			BlockNumber:  blockNumber,
			FromAddress:  strings.ToLower(fromAddr),
			ToAddress:    strings.ToLower(toAddr),
			AssetAddress: strings.ToLower(log.Address), // ERC-20 contract address
			Amount:       amount.String(),              // Amount in token's smallest unit
			Type:         "erc20",
			TxFee:        txFee,
			Timestamp:    timestamp,
		})
	}

	return transfers
}
