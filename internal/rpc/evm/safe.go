package evm

import (
	"encoding/hex"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"golang.org/x/crypto/sha3"
)

// SafeSingletonSlot is the storage slot where a Safe proxy keeps its singleton address.
const SafeSingletonSlot = "0x0"

// keccak256 of official Safe proxy runtime code, derived from each factory's proxyCreationCode().
var safeProxyCodeHashes = map[string]bool{
	"0xaea7d4252f6245f301e540cfbee27d3a88de543af8e49c5c62405d5499fab7e5": true, // v1.1.1
	"0xb89c1b3bdf2cf8827818646bce9a8f6e372885f8c55e5c07acbd307cb133b000": true, // v1.3.0
	"0xd7d408ebcd99b2b70be43e20253d6d92a8ea8fab29bd3be7f55b10032331fb4c": true, // v1.4.1
	"0x4e381985ca68b3e5d27b4425fa581c19cf33146d3f887a3cfca96f55528ea46f": true, // v1.5.0
}

// From safe-global/safe-deployments; zkSync Era and v1.0.0 (no ExecutionSuccess) are not covered.
var safeSingletons = map[string]bool{
	"0x34cfac646f301356faa8b21e94227e3583fe3f5f": true, // v1.1.1 GnosisSafe
	"0x6851d6fdfafd08c0295c392436245e5bc78b0185": true, // v1.2.0 GnosisSafe (uses the v1.1.1 proxy)
	"0xd9db270c1b5e3bd161e8c8503c55ceabee709552": true, // v1.3.0 GnosisSafe
	"0x69f4d1788e39c87893c980c06edf4b7f686e2938": true, // v1.3.0 GnosisSafe (eip155)
	"0x3e5c63644e683549055b9be8653de26e0b4cd36e": true, // v1.3.0 GnosisSafeL2
	"0xfb1bffc9d739b8d520daf37df666da4c687191ea": true, // v1.3.0 GnosisSafeL2 (eip155)
	"0x41675c099f32341bf84bfc5382af534df5c7461a": true, // v1.4.1 Safe
	"0x29fcb43b46531bca003ddc8fcb67ffe91900c762": true, // v1.4.1 SafeL2
	"0xff51a5898e281db6dfc7855790607438df2ca44b": true, // v1.5.0 Safe
	"0xedd160febbd92e350d4d398fb636302fccd67c7e": true, // v1.5.0 SafeL2
}

func IsCanonicalSafe(code, slot0 string) bool {
	return IsSafeProxyCode(code) && IsSafeSingletonSlot(slot0)
}

func IsSafeProxyCode(code string) bool {
	codeBytes, err := hex.DecodeString(strings.TrimPrefix(code, "0x"))
	if err != nil || len(codeBytes) == 0 {
		return false
	}
	hash := sha3.NewLegacyKeccak256()
	hash.Write(codeBytes)
	return safeProxyCodeHashes["0x"+hex.EncodeToString(hash.Sum(nil))]
}

func IsSafeSingletonSlot(slot0 string) bool {
	word := strings.ToLower(strings.TrimPrefix(slot0, "0x"))
	if len(word) != 64 || strings.Trim(word[:24], "0") != "" {
		return false
	}
	return safeSingletons["0x"+word[24:]]
}

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
//   - tx.To was confirmed as an official Safe around this block (isCanonicalSafe)
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
	isCanonicalSafe bool,
) []types.Transaction {
	if receipt == nil || !isCanonicalSafe {
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

	fee := tx.CalcFee(receipt)
	txIdx := hexIndexToDecimal(tx.TransactionIndex)

	return []types.Transaction{
		{
			TxHash:        tx.Hash,
			NetworkId:     network,
			BlockNumber:   blockNumber,
			TransferIndex: txIdx + ":safe:0",
			FromAddress:   ToChecksumAddress(tx.To), // Safe contract is the sender
			ToAddress:     params.To,                // decoded recipient
			Amount:        params.Value.String(),
			Type:          constant.TxTypeNativeTransfer,
			TxFee:         fee,
			Timestamp:     ts,
		},
	}
}
