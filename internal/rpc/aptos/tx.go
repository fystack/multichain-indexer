package aptos

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const (
	APTDecimals   = 8
	NativeAPTType = "0x1::aptos_coin::AptosCoin"
)

func (tx *Transaction) ExtractTransfers(networkId string, blockNumber uint64) []types.Transaction {
	if !tx.Success || tx.Type != TxTypeUser {
		return nil
	}

	var transfers []types.Transaction
	fee := tx.calculateFee()
	timestamp := parseTimestamp(tx.Timestamp)

	withdrawMap := make(map[string]map[string]string)
	depositMap := make(map[string]map[string]string)

	for _, event := range tx.Events {
		switch {
		case strings.Contains(event.Type, "WithdrawEvent"):
			coinType := extractCoinType(event.Type)
			addr := NormalizeAddress(event.GUID.AccountAddress)
			if withdrawMap[addr] == nil {
				withdrawMap[addr] = make(map[string]string)
			}
			amount := event.Data.Amount
			if existing, ok := withdrawMap[addr][coinType]; ok {
				existingDec, _ := decimal.NewFromString(existing)
				newDec, _ := decimal.NewFromString(amount)
				withdrawMap[addr][coinType] = existingDec.Add(newDec).String()
			} else {
				withdrawMap[addr][coinType] = amount
			}

		case strings.Contains(event.Type, "DepositEvent"):
			coinType := extractCoinType(event.Type)
			addr := NormalizeAddress(event.GUID.AccountAddress)
			if depositMap[addr] == nil {
				depositMap[addr] = make(map[string]string)
			}
			amount := event.Data.Amount
			if existing, ok := depositMap[addr][coinType]; ok {
				existingDec, _ := decimal.NewFromString(existing)
				newDec, _ := decimal.NewFromString(amount)
				depositMap[addr][coinType] = existingDec.Add(newDec).String()
			} else {
				depositMap[addr][coinType] = amount
			}
		}
	}

	for toAddr, coins := range depositMap {
		for coinType, amount := range coins {
			fromAddr := tx.Sender
			if fromAddr == "" {
				fromAddr = findWithdrawAddress(withdrawMap, coinType, amount)
			}

			transfer := types.Transaction{
				TxHash:      tx.Hash,
				NetworkId:   networkId,
				BlockNumber: blockNumber,
				FromAddress: NormalizeAddress(fromAddr),
				ToAddress:   NormalizeAddress(toAddr),
				Amount:      amount,
				TxFee:       fee,
				Timestamp:   timestamp,
			}

			if isNativeAPT(coinType) {
				transfer.AssetAddress = ""
				transfer.Type = constant.TxTypeNativeTransfer
			} else {
				transfer.AssetAddress = coinType
				transfer.Type = constant.TxTypeTokenTransfer
			}

			transfers = append(transfers, transfer)
		}
	}

	return transfers
}

func (tx *Transaction) calculateFee() decimal.Decimal {
	gasUsed, _ := strconv.ParseInt(tx.GasUsed, 10, 64)
	gasPrice, _ := strconv.ParseInt(tx.GasUnitPrice, 10, 64)

	feeOctas := gasUsed * gasPrice
	feeAPT := decimal.NewFromInt(feeOctas).Div(decimal.NewFromInt(100000000))

	return feeAPT
}

func parseTimestamp(ts string) uint64 {
	microseconds, err := strconv.ParseUint(ts, 10, 64)
	if err != nil {
		return 0
	}
	return microseconds / 1000000
}

func extractCoinType(eventType string) string {
	start := strings.Index(eventType, "<")
	end := strings.LastIndex(eventType, ">")
	if start == -1 || end == -1 || start >= end {
		return NativeAPTType
	}
	return strings.TrimSpace(eventType[start+1 : end])
}

func isNativeAPT(coinType string) bool {
	return coinType == NativeAPTType || coinType == ""
}

func findWithdrawAddress(withdrawMap map[string]map[string]string, coinType, amount string) string {
	for addr, coins := range withdrawMap {
		if withdrawAmount, ok := coins[coinType]; ok && withdrawAmount == amount {
			return addr
		}
	}
	return ""
}

func ParseVersion(versionStr string) (uint64, error) {
	version, err := strconv.ParseUint(versionStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid version: %w", err)
	}
	return version, nil
}
