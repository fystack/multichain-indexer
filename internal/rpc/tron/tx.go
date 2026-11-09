package tron

import (
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/types"
)

// IsSuccessful checks if a transaction succeeded
func (tx *Txn) IsSuccessful() bool {
	if tx == nil {
		return false // nil Txn = invalid / not found
	}

	// If Ret missing or empty => native TRX transfer (implicitly success)
	if len(tx.Ret) == 0 {
		return true
	}

	// Normalize and check contractRet
	ret := strings.ToUpper(strings.TrimSpace(tx.Ret[0].ContractRet))
	switch ret {
	case "SUCCESS":
		return true
	case "":
		// Empty contractRet can happen for pending or malformed tx → not success
		return false
	default:
		// Explicit revert or failure types
		return false
	}
}

// ExtractTransfers collects transfers from txn + receipt info
func (tx *Txn) ExtractTransfers(network, blockHash string, info *TxnInfo, ts uint64) []types.Transaction {
	var out []types.Transaction
	if info == nil {
		return out
	}
	// fees handled at indexer level
	for logIndex, log := range info.Log {
		if transfers, _ := log.ParseTRC20Transfers(tx.TxID, network, blockHash, uint64(info.BlockNumber), ts, logIndex); transfers != nil {
			out = append(out, transfers...)
		}
	}
	return out
}
