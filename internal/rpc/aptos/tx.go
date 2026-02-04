package aptos

import (
	"encoding/json"
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
	// NativeAptosFA is the object address for native APT as a Fungible Asset (FA standard)
	NativeAptosFA = "0x000000000000000000000000000000000000000000000000000000000000000a"
)

func (tx *Transaction) ExtractTransfers(networkId string, blockNumber uint64) []types.Transaction {
	if !tx.Success || tx.Type != TxTypeUser {
		return nil
	}

	var transfers []types.Transaction
	fee := tx.calculateFee()
	timestamp := parseTimestamp(tx.Timestamp)

	storeOwners := tx.buildStoreOwnerMap()
	storeMetadata := tx.buildStoreMetadataMap()

	// Key: address -> assetType -> amount
	withdrawMap := make(map[string]map[string]string)
	depositMap := make(map[string]map[string]string)

	for _, event := range tx.Events {
		switch {
		// Legacy Coin events
		case strings.Contains(event.Type, "::coin::WithdrawEvent"):
			coinType := extractCoinType(event.Type)
			addr := NormalizeAddress(event.GUID.AccountAddress)
			amount := event.Data.Amount
			addToMap(withdrawMap, addr, coinType, amount)

		case strings.Contains(event.Type, "::coin::DepositEvent"):
			coinType := extractCoinType(event.Type)
			addr := NormalizeAddress(event.GUID.AccountAddress)
			amount := event.Data.Amount
			addToMap(depositMap, addr, coinType, amount)

		// FA Events
		case strings.Contains(event.Type, "::fungible_asset::Withdraw"):
			storeAddr := NormalizeAddress(event.Data.Store)
			ownerAddr := storeOwners[storeAddr]
			if ownerAddr == "" {
				ownerAddr = tx.Sender
			}
			faType := extractFATypeFromMetadata(storeMetadata[storeAddr])
			amount := extractFAAmount(event.Data)
			addToMap(withdrawMap, ownerAddr, faType, amount)

		case strings.Contains(event.Type, "::fungible_asset::Deposit"):
			storeAddr := NormalizeAddress(event.Data.Store)
			ownerAddr := storeOwners[storeAddr]
			if ownerAddr == "" {
				ownerAddr = tx.Sender
			}
			faType := extractFATypeFromMetadata(storeMetadata[storeAddr])
			amount := extractFAAmount(event.Data)
			addToMap(depositMap, ownerAddr, faType, amount)
		}
	}

	for toAddr, assets := range depositMap {
		for assetType, amount := range assets {
			fromAddr := tx.Sender
			if fromAddr == "" {
				fromAddr = findWithdrawAddress(withdrawMap, assetType, amount)
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

			if isNativeAPT(assetType) || isNativeAptosFA(assetType) {
				transfer.AssetAddress = ""
				transfer.Type = constant.TxTypeNativeTransfer
			} else {
				transfer.AssetAddress = assetType
				transfer.Type = constant.TxTypeTokenTransfer
			}

			transfers = append(transfers, transfer)
		}
	}

	return transfers
}

// buildStoreOwnerMap builds a map of store addresses to owner addresses
func (tx *Transaction) buildStoreOwnerMap() map[string]string {
	storeOwners := make(map[string]string)

	for _, change := range tx.Changes {
		var changeMap map[string]interface{}
		if err := json.Unmarshal(change, &changeMap); err != nil {
			continue
		}

		changeType, _ := changeMap["type"].(string)
		if changeType != "write_resource" {
			continue
		}

		data, ok := changeMap["data"].(map[string]interface{})
		if !ok {
			continue
		}

		dataType, _ := data["type"].(string)
		if dataType != "0x1::object::ObjectCore" {
			continue
		}

		storeAddr, _ := changeMap["address"].(string)
		if storeAddr == "" {
			continue
		}

		innerData, ok := data["data"].(map[string]interface{})
		if !ok {
			continue
		}

		owner, _ := innerData["owner"].(string)
		if owner != "" {
			storeOwners[NormalizeAddress(storeAddr)] = NormalizeAddress(owner)
		}
	}

	return storeOwners
}

// buildStoreMetadataMap builds a map of store addresses to metadata addresses
func (tx *Transaction) buildStoreMetadataMap() map[string]string {
	storeMetadata := make(map[string]string)

	for _, change := range tx.Changes {
		var changeMap map[string]interface{}
		if err := json.Unmarshal(change, &changeMap); err != nil {
			continue
		}

		changeType, _ := changeMap["type"].(string)
		if changeType != "write_resource" {
			continue
		}

		data, ok := changeMap["data"].(map[string]interface{})
		if !ok {
			continue
		}

		dataType, _ := data["type"].(string)
		if dataType != "0x1::fungible_asset::FungibleStore" {
			continue
		}

		storeAddr, _ := changeMap["address"].(string)
		if storeAddr == "" {
			continue
		}

		innerData, ok := data["data"].(map[string]interface{})
		if !ok {
			continue
		}

		metadata, ok := innerData["metadata"].(map[string]interface{})
		if !ok {
			continue
		}

		metadataInner, _ := metadata["inner"].(string)
		if metadataInner != "" {
			storeMetadata[NormalizeAddress(storeAddr)] = NormalizeAddress(metadataInner)
		}
	}

	return storeMetadata
}

func addToMap(m map[string]map[string]string, addr, assetType, amount string) {
	if m[addr] == nil {
		m[addr] = make(map[string]string)
	}
	if existing, ok := m[addr][assetType]; ok {
		existingDec, _ := decimal.NewFromString(existing)
		newDec, _ := decimal.NewFromString(amount)
		m[addr][assetType] = existingDec.Add(newDec).String()
	} else {
		m[addr][assetType] = amount
	}
}

// extractFATypeFromMetadata extracts the FA type from the store metadata address
func extractFATypeFromMetadata(metadata string) string {
	if metadata == "" {
		return NativeAptosFA
	}
	normalized := NormalizeAddress(metadata)
	if normalized == NativeAptosFA || normalized == "0x000000000000000000000000000000000000000000000000000000000000000a" {
		return NativeAPTType // Return legacy type for native APT
	}
	return normalized
}

// extractFAAmount extracts amount from FA event data
// FA events may have different data structures than Coin events
func extractFAAmount(data EventData) string {
	if data.Amount != "" {
		return data.Amount
	}
	return "0"
}

func isNativeAptosFA(assetType string) bool {
	// Native APT as FA has metadata at 0xa
	return assetType == NativeAptosFA ||
		assetType == "0x0a" ||
		strings.HasSuffix(assetType, "::aptos_coin::AptosCoin")
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
	// Native APT can be represented as:
	// 1. Legacy: "0x1::aptos_coin::AptosCoin"
	// 2. FA: "0x0000...000a" (the metadata address)
	return coinType == NativeAPTType ||
		coinType == NativeAptosFA ||
		coinType == "0x000000000000000000000000000000000000000000000000000000000000000a" ||
		coinType == ""
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
