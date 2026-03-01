package evm

import (
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
)

// IsSafeExecTransaction checks if the tx input starts with the Gnosis Safe
// execTransaction method selector (0x6a761202).
func IsSafeExecTransaction(input string) bool {
	return len(input) >= 10 && input[:10] == SAFE_EXEC_TRANSACTION_SIG
}

// ExtractSafeTransfers extracts native ETH transfers from a Gnosis Safe
// execTransaction call. This is a standalone function that does NOT call
// ExtractTransfers() — Safe transactions are processed in an isolated
// pipeline to avoid any side effects on existing transfer detection.
//
// Returns a native_transfer if all conditions are met:
//   - Receipt contains ExecutionSuccess event emitted by the Safe contract (tx.To)
//   - Decoded operation == 0 (Call, not DelegateCall)
//   - Decoded value > 0 (ETH is being sent)
//   - Decoded data is empty (pure ETH transfer, not a contract call)
//
// NOTE: When data is non-empty (value + contract call), the native ETH portion
// is NOT extracted. This is a known limitation — see "Known Limitations" in the plan.
func ExtractSafeTransfers(
	tx Txn,
	receipt *TxnReceipt,
	network string,
	blockNumber, ts uint64,
) []types.Transaction {
	if receipt == nil {
		return nil
	}

	// Must have ExecutionSuccess event emitted by the Safe contract (tx.To).
	// Checking the emitter address prevents false matches from events emitted
	// by other contracts called within the same transaction.
	if !receipt.HasLogTopicFrom(SAFE_EXECUTION_SUCCESS_TOPIC, tx.To) {
		return nil
	}

	params, err := DecodeGnosisSafeExecTransaction(tx.Input)
	if err != nil {
		return nil
	}

	// Only handle Call operations (operation == 0), skip DelegateCall
	if params.Operation != 0 {
		return nil
	}

	// Skip if no ETH value
	if params.Value.Sign() <= 0 {
		return nil
	}

	// Pure native ETH transfer: empty inner data + value > 0
	if len(params.Data) != 0 {
		return nil
	}

	fee := tx.calcFee(receipt)

	return []types.Transaction{
		{
			TxHash:      tx.Hash,
			NetworkId:   network,
			BlockNumber: blockNumber,
			FromAddress: ToChecksumAddress(tx.To), // Safe contract is the sender
			ToAddress:   params.To,                // decoded recipient
			Amount:      params.Value.String(),
			Type:        constant.TxTypeNativeTransfer,
			TxFee:       fee,
			Timestamp:   ts,
		},
	}
}
