package tron

import (
	"github.com/fystack/multichain-indexer/pkg/common/types"
)

// IsSuccessful checks if a transaction succeeded
func (tx *Txn) IsSuccessful() bool {
	if tx == nil || len(tx.Ret) == 0 {
		return true
	}
	ret := tx.Ret[0]
	return ret.ContractRet == "SUCCESS" || ret.ContractRet == ""
}

// ExtractTransfers collects transfers from txn + receipt info
func (tx *Txn) ExtractTransfers(network string, info *TxnInfo, ts uint64) []types.Transaction {
	var out []types.Transaction
	if info == nil {
		return out
	}
	// fees handled at indexer level
	for _, log := range info.Log {
		if transfers, _ := log.ParseTRC20Transfers(tx.TxID, network, uint64(info.BlockNumber), ts); transfers != nil {
			out = append(out, transfers...)
		}
	}
	return out
}
