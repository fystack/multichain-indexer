package evm

import (
	"strconv"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
)

// ExtractInternalTransfers extracts native value transfers from a debug_traceTransaction call tree.
// Returns transfers as native_transfer type (same as ExtractTransfers) for downstream compatibility.
//
// Root call dedup: the root CALL is skipped when the top-level tx is a plain native transfer
// (Input == "" && To != ""), because ExtractTransfers() already captures it.
// For contract calls with value (non-empty input), the root is NOT skipped.
// For CREATE txs (To == ""), the root is NOT skipped — ExtractTransfers() doesn't emit
// a native transfer for CREATE txs, so the trace entry is a genuinely new event.
func ExtractInternalTransfers(
	trace *CallTrace,
	tx Txn,
	fee decimal.Decimal,
	networkId string,
	blockNumber, timestamp uint64,
) []types.Transaction {
	if trace == nil {
		return nil
	}

	// Skip root only for plain native transfers where ExtractTransfers already emits one.
	isPlainNativeTransfer := tx.To != "" && (strings.TrimPrefix(tx.Input, "0x") == "")
	skipRoot := isPlainNativeTransfer

	txIdx := hexIndexToDecimal(tx.TransactionIndex)
	counter := 0
	var out []types.Transaction
	walkCallTrace(trace, tx.Hash, fee, networkId, blockNumber, timestamp, skipRoot, true, &out, txIdx, &counter)
	return out
}

// walkCallTrace recursively walks the call trace tree and extracts value transfers.
func walkCallTrace(
	call *CallTrace,
	txHash string,
	fee decimal.Decimal,
	networkId string,
	blockNumber, timestamp uint64,
	skipRoot bool,
	isRoot bool,
	out *[]types.Transaction,
	txIdx string,
	counter *int,
) {
	if call == nil {
		return
	}

	// Skip failed calls — reverted sub-calls don't transfer value
	if call.Error != "" {
		return
	}

	shouldExtract := false
	if !isRoot || !skipRoot {
		switch call.Type {
		case "CALL", "CREATE", "CREATE2":
			shouldExtract = true
		// DELEGATECALL: no value transfer (runs in caller's context)
		// STATICCALL: read-only, cannot transfer value
		}
	}

	if shouldExtract {
		val, err := utils.ParseHexBigInt(call.Value)
		if err == nil && val.Sign() > 0 {
			*out = append(*out, types.Transaction{
				TxHash:        txHash,
				NetworkId:     networkId,
				BlockNumber:   blockNumber,
				TransferIndex: txIdx + ":trace:" + strconv.Itoa(*counter),
				FromAddress:   ToChecksumAddress(call.From),
				ToAddress:     ToChecksumAddress(call.To),
				Amount:        val.String(),
				Type:          constant.TxTypeNativeTransfer,
				TxFee:         fee,
				Timestamp:     timestamp,
			})
			*counter++
		}
	}

	// Recurse into child calls
	for i := range call.Calls {
		walkCallTrace(&call.Calls[i], txHash, fee, networkId, blockNumber, timestamp, false, false, out, txIdx, counter)
	}
}
