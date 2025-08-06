package tron

import (
	"math/big"
	"strings"
)

// TRC20TransferData represents decoded TRC-20 transfer data
type TRC20TransferData struct {
	ToAddress string
	Amount    *big.Int
}

// TRC-20 function selectors
const (
	TransferSelector     = "a9059cbb" // transfer(address,uint256)
	TransferFromSelector = "23b872dd" // transferFrom(address,address,uint256)
)

// decodeTRC20TransferData decodes TRC-20 transfer data from hex string
func decodeTRC20TransferData(data string) *TRC20TransferData {
	// Remove 0x prefix if present
	data = strings.TrimPrefix(data, "0x")

	// Check for minimum length
	if len(data) < 74 { // 4 bytes selector + 32 bytes address + 32 bytes amount
		return nil
	}

	// Check for transfer function
	if strings.HasPrefix(data, TransferSelector) {
		return decodeTransfer(data)
	}

	// Check for transferFrom function
	if strings.HasPrefix(data, TransferFromSelector) {
		return decodeTransferFrom(data)
	}

	return nil
}

// decodeTransfer decodes transfer(address,uint256) function
func decodeTransfer(data string) *TRC20TransferData {
	// Extract to address (32 bytes starting at position 8)
	toAddressHex := data[8:72]
	toAddress := convertToTronAddress(toAddressHex)

	// Extract amount (32 bytes starting at position 72)
	amountHex := data[72:136]
	amount, ok := new(big.Int).SetString(amountHex, 16)
	if !ok {
		return nil
	}

	return &TRC20TransferData{
		ToAddress: toAddress,
		Amount:    amount,
	}
}

// decodeTransferFrom decodes transferFrom(address,address,uint256) function
func decodeTransferFrom(data string) *TRC20TransferData {
	// Extract to address (32 bytes starting at position 72, second address)
	toAddressHex := data[72:136]
	toAddress := convertToTronAddress(toAddressHex)

	// Extract amount (32 bytes starting at position 136, third parameter)
	if len(data) < 168 {
		return nil
	}
	amountHex := data[136:168]
	amount, ok := new(big.Int).SetString(amountHex, 16)
	if !ok {
		return nil
	}

	return &TRC20TransferData{
		ToAddress: toAddress,
		Amount:    amount,
	}
}

// convertToTronAddress converts hex address to Tron address format
func convertToTronAddress(hexAddress string) string {
	// Remove leading zeros
	hexAddress = strings.TrimLeft(hexAddress, "0")
	if len(hexAddress) == 0 {
		hexAddress = "0"
	}

	// Ensure even length
	if len(hexAddress)%2 != 0 {
		hexAddress = "0" + hexAddress
	}

	// Convert to Tron address format
	return "T" + hexAddress
}
