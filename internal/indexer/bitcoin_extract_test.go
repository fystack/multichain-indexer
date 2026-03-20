package indexer

// bitcoin_extract_test.go: Tests for Bitcoin transaction extraction logic.
//
// MISSING / INCORRECT TRANSACTION CASES IDENTIFIED:
//
//  1. Multi-input: non-first sender invisible to TwoWayIndexing
//     fromAddr is always the FIRST input's address. If a monitored address
//     contributes input[1+], its outgoing transfer is never detected.
//
//  2. Input-address-also-receives filtered as "change"
//     IndexChangeOutput=false filters any output whose address appears in
//     the input set, even when that output is a net incoming payment (e.g.
//     another party sends to this address in the same tx). The transfer is
//     silently dropped from the Transfer event stream.
//
//  3. Bare multisig (P2MS) outputs: only Addresses[0] captured
//     GetOutputAddress returns the first element of ScriptPubKey.Addresses.
//     Monitored addresses at index 1+ are missed.
//
//  4. Taproot (P2TR) bech32m: NormalizeBTCAddress returns an error
//     github.com/btcsuite/btcutil/bech32 v1.0.2 only supports BIP-173
//     (bech32). bc1p… addresses use BIP-350 (bech32m) and fail Decode.
//     The code gracefully falls back to the raw address, so transfers are
//     NOT lost in practice, but the function incorrectly rejects valid
//     Taproot addresses.
//
//  5. Partial prevout-resolution check (Vin[0] only)
//     Both convertBlockWithPrevoutResolution and ResolvePrevouts skip a
//     transaction if Vin[0].PrevOut != nil. If Vin[0] has prevout data but
//     Vin[1+] do not, the later inputs are never resolved: their FromAddress
//     is empty in UTXO Spent events, and they contribute 0 to fee calculation.
//
//  6. Self-consolidation produces zero Transfer events
//     With IndexChangeOutput=false a tx that sweeps funds back to the same
//     address emits no Transfer events. UTXO events still capture it.
//
//  7. Coinbase skipped  – correct ✓
//  8. OP_RETURN skipped – correct ✓

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/bitcoin"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ────────────────────────────────────────────────────────────────

// newBTCTestIndexer creates a BitcoinIndexer suitable for unit tests.
// No failover/network is needed when calling extractTransfersFromTx /
// extractUTXOEvent directly.
func newBTCTestIndexer(cfg config.ChainConfig) *BitcoinIndexer {
	return &BitcoinIndexer{
		chainName: "bitcoin_testnet",
		config:    cfg,
	}
}

// btcInput builds an Input with fully resolved PrevOut data.
func btcInput(prevTxID string, prevVout uint32, addr string, valueBTC float64) bitcoin.Input {
	return bitcoin.Input{
		TxID: prevTxID,
		Vout: prevVout,
		PrevOut: &bitcoin.Output{
			Value: valueBTC,
			ScriptPubKey: bitcoin.ScriptPubKey{
				Address: addr,
			},
		},
	}
}

// btcOutput builds a standard pay-to-address output.
func btcOutput(addr string, valueBTC float64, n uint32) bitcoin.Output {
	return bitcoin.Output{
		Value: valueBTC,
		N:     n,
		ScriptPubKey: bitcoin.ScriptPubKey{
			Address: addr,
		},
	}
}

// btcOpReturnOutput builds an OP_RETURN output (no spendable address).
func btcOpReturnOutput(n uint32) bitcoin.Output {
	return bitcoin.Output{
		Value: 0,
		N:     n,
		ScriptPubKey: bitcoin.ScriptPubKey{
			Type: "nulldata",
			ASM:  "OP_RETURN 68656c6c6f",
			Hex:  "6a0568656c6c6f",
			// Address and Addresses intentionally empty
		},
	}
}

// btcMultisigOutput builds a bare-multisig output with multiple addresses.
func btcMultisigOutput(addrs []string, valueBTC float64, n uint32) bitcoin.Output {
	return bitcoin.Output{
		Value: valueBTC,
		N:     n,
		ScriptPubKey: bitcoin.ScriptPubKey{
			Type:      "multisig",
			Addresses: addrs,
			// Address field empty – uses Addresses slice
		},
	}
}

// ─── satoshisFromFloat precision ────────────────────────────────────────────

func TestBitcoinSatoshisFromFloat_Precision(t *testing.T) {
	tests := []struct {
		btc  float64
		want int64
	}{
		{0.00000001, 1},          // 1 satoshi
		{0.1, 10_000_000},        // 0.1 BTC – classic float truncation case
		{0.00100000, 100_000},    // 0.001 BTC
		{1.0, 100_000_000},       // 1 BTC
		{21.0, 2_100_000_000},    // 21 BTC
		{0.29300000, 29_300_000}, // 0.293 BTC (float64 is 0.2929999…)
		{0.00000100, 100},        // 100 satoshis
	}
	for _, tc := range tests {
		got := satoshisFromFloat(tc.btc)
		assert.Equal(t, tc.want, got,
			"satoshisFromFloat(%.8f) should be %d", tc.btc, tc.want)
	}
}

// ─── fee calculation ────────────────────────────────────────────────────────

func TestBitcoinCalculateFee_MultiInput(t *testing.T) {
	tx := &bitcoin.Transaction{
		TxID: "fee_test",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "addr_A", 0.3),
			btcInput("p2", 0, "addr_B", 0.2),
			btcInput("p3", 1, "addr_C", 0.15),
		},
		Vout: []bitcoin.Output{
			btcOutput("addr_D", 0.64, 0),
		},
	}

	fee := tx.CalculateFee()
	want := decimal.RequireFromString("0.01")
	assert.True(t, want.Equal(fee),
		"fee: want %s got %s", want, fee)
}

func TestBitcoinCalculateFee_Negative_Clamped(t *testing.T) {
	// Outputs > Inputs (should never happen in a valid tx, but clamp to zero)
	tx := &bitcoin.Transaction{
		TxID: "neg_fee",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "addr_A", 0.1),
		},
		Vout: []bitcoin.Output{
			btcOutput("addr_B", 0.5, 0),
		},
	}
	assert.True(t, decimal.Zero.Equal(tx.CalculateFee()))
}

func TestBitcoinCalculateFee_MissingPrevout_SkipsInput(t *testing.T) {
	// If an input has no PrevOut, its value isn't counted → under-counted fee
	// This documents case #5 (partial prevout resolution side-effect).
	tx := &bitcoin.Transaction{
		TxID: "missing_prevout",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "addr_A", 0.5),
			// Vin[1] has no PrevOut – its 0.1 BTC contribution is invisible
			{TxID: "p2", Vout: 0},
		},
		Vout: []bitcoin.Output{
			btcOutput("addr_B", 0.59, 0),
		},
	}

	fee := tx.CalculateFee()
	// Only Vin[0] (0.5) counted – fee appears as 0.5-0.59 = negative → clamped to 0
	// (real fee should be 0.5+0.1-0.59=0.01 but Vin[1] isn't counted)
	assert.True(t, decimal.Zero.Equal(fee),
		"fee with missing prevout should be clamped to 0, got %s", fee)
}

// ─── extractTransfersFromTx – unit tests ────────────────────────────────────

func TestBitcoinExtractTransfers_SimpleP2PKH_ChangeFiltered(t *testing.T) {
	// Simple 1-input, 2-output tx: payment + change back to sender.
	// With IndexChangeOutput=false the change output is filtered.
	const addrSender = "sender_alice"
	const addrRecipient = "recipient_bob"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: false,
	})

	tx := &bitcoin.Transaction{
		TxID: "simple_tx",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, addrSender, 0.5)},
		Vout: []bitcoin.Output{
			btcOutput(addrRecipient, 0.39, 0),
			btcOutput(addrSender, 0.1, 1), // change
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1, "only the payment output should produce a transfer")
	assert.Equal(t, addrRecipient, transfers[0].ToAddress)
	assert.Equal(t, addrSender, transfers[0].FromAddress)
	assert.Equal(t, "39000000", transfers[0].Amount, "0.39 BTC = 39000000 sat")
	assert.Equal(t, constant.TxTypeNativeTransfer, transfers[0].Type)
	assert.False(t, transfers[0].TxFee.IsZero(), "fee should be assigned to first transfer")
}

func TestBitcoinExtractTransfers_SimpleP2PKH_ChangeIncluded(t *testing.T) {
	// With IndexChangeOutput=true, both outputs create transfers.
	const addrSender = "sender_alice"
	const addrRecipient = "recipient_bob"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "simple_tx_v2",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, addrSender, 0.5)},
		Vout: []bitcoin.Output{
			btcOutput(addrRecipient, 0.39, 0),
			btcOutput(addrSender, 0.1, 1),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 2)
	// Fee only on the first transfer
	assert.False(t, transfers[0].TxFee.IsZero())
	assert.True(t, transfers[1].TxFee.IsZero())
}

// --- KNOWN BUG #1 -----------------------------------------------------------

func TestBitcoinExtractTransfers_MultiInput_FromAddrAlwaysFirstInput(t *testing.T) {
	// All transfers produced for this tx show FromAddress = first input's address,
	// regardless of how many inputs there are.
	const addrFirst = "sender_first_input"
	const addrSecond = "sender_second_input"
	const addrRecipient = "recipient_carol"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "multi_input_tx",
		Vin: []bitcoin.Input{
			btcInput("prev1", 0, addrFirst, 0.3),
			btcInput("prev2", 0, addrSecond, 0.2),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrRecipient, 0.49, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, addrFirst, transfers[0].FromAddress,
		"FromAddress should always be the FIRST input's address")

	// Verify addrSecond (second input) never appears as FromAddress.
	for _, tr := range transfers {
		assert.NotEqual(t, addrSecond, tr.FromAddress,
			"second input's address is invisible in Transfer events (known limitation)")
	}
}

func TestBitcoinExtractTransfers_MultiInput_SecondSenderInvisibleToTwoWayIndexing(t *testing.T) {
	// KNOWN BUG #1 (detailed scenario):
	// If a monitored address contributes input[1+] of a multi-input transaction,
	// its outgoing transfer will never be emitted even when TwoWayIndexing=true.
	// The worker's emitBlock checks tx.FromAddress against pubkeyStore; since
	// FromAddress is always input[0]'s address, input[1]'s address is never checked.
	//
	// Mitigation: the UTXO Spent event stream correctly captures all inputs.
	const addrNonMonitored = "sender_first_not_monitored"
	const addrMonitored = "sender_second_MONITORED"
	const addrRecipient = "recipient_dave"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "two_way_bug_tx",
		Vin: []bitcoin.Input{
			btcInput("prev1", 0, addrNonMonitored, 0.5),
			btcInput("prev2", 0, addrMonitored, 0.3), // monitored, but NOT first input
		},
		Vout: []bitcoin.Output{
			btcOutput(addrRecipient, 0.79, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1)

	// The only Transfer record's FromAddress is the non-monitored first input.
	// A worker with TwoWayIndexing and pubkeyStore containing addrMonitored would
	// check transfers[0].FromAddress == addrNonMonitored → not monitored → outgoing
	// transfer from addrMonitored is silently dropped.
	fromAddresses := make(map[string]bool)
	for _, tr := range transfers {
		fromAddresses[tr.FromAddress] = true
	}
	assert.False(t, fromAddresses[addrMonitored],
		"addrMonitored (second input) never appears as FromAddress – its outgoing transfer would be missed by TwoWayIndexing")
}

// --- KNOWN BUG #2 -----------------------------------------------------------

func TestBitcoinExtractTransfers_InputAddrAlsoReceives_FilteredAsChange(t *testing.T) {
	// KNOWN BUG #2:
	// When IndexChangeOutput=false, any output whose address appears in the
	// input set is classified as "change" and filtered, even if that address
	// is receiving a net payment from another party in the same transaction.
	//
	// Example: A and B both contribute inputs. B also receives an output
	// larger than their input (a net payment to B). The B→B output is
	// filtered because B is in inputAddrs.
	const addrA = "sender_alice"
	const addrB = "sender_and_receiver_bob" // in both inputs and outputs
	const addrC = "recipient_carol"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: false,
	})

	tx := &bitcoin.Transaction{
		TxID: "input_also_receives_tx",
		Vin: []bitcoin.Input{
			btcInput("prev1", 0, addrA, 0.5),
			btcInput("prev2", 0, addrB, 0.1), // B contributes only 0.1 BTC
		},
		Vout: []bitcoin.Output{
			btcOutput(addrB, 0.45, 0), // B receives 0.45 BTC – net payment to B!
			btcOutput(addrC, 0.14, 1),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	// Without the bug: 2 transfers (A→B for 0.45, A→C for 0.14)
	// With the bug:    1 transfer  (A→C for 0.14) – A→B dropped as "change"
	toAddresses := make(map[string]bool)
	for _, tr := range transfers {
		toAddresses[tr.ToAddress] = true
	}

	assert.False(t, toAddresses[addrB],
		"KNOWN BUG #2: output to addrB is incorrectly filtered as change "+
			"even though B receives a net incoming payment in this tx")
	assert.True(t, toAddresses[addrC],
		"output to addrC should still be captured")
	assert.Len(t, transfers, 1,
		"only 1 transfer captured (A→C); the A→B net payment to B is silently dropped")
}

// --- KNOWN BUG #3 -----------------------------------------------------------

func TestBitcoinExtractTransfers_BareMultisig_OnlyFirstAddressCaptured(t *testing.T) {
	// KNOWN BUG #3:
	// GetOutputAddress returns only Addresses[0] for bare multisig outputs.
	// Monitored addresses at index 1+ are missed.
	// Note: bare multisig outputs are extremely rare on mainnet; P2SH/P2WSH
	// wrapping is standard. This is a low-severity limitation.
	const addrSender = "sender_alice"
	const msigAddr0 = "multisig_participant_0"
	const msigAddr1 = "multisig_participant_1" // will be missed
	const msigAddr2 = "multisig_participant_2" // will be missed

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "bare_multisig_tx",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, addrSender, 1.0)},
		Vout: []bitcoin.Output{
			btcMultisigOutput([]string{msigAddr0, msigAddr1, msigAddr2}, 0.99, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, msigAddr0, transfers[0].ToAddress,
		"only the first multisig participant is captured")

	// msigAddr1 and msigAddr2 never appear
	for _, tr := range transfers {
		assert.NotEqual(t, msigAddr1, tr.ToAddress,
			"KNOWN BUG #3: multisig participant at index 1 is invisible")
		assert.NotEqual(t, msigAddr2, tr.ToAddress,
			"KNOWN BUG #3: multisig participant at index 2 is invisible")
	}
}

// --- batch payment -----------------------------------------------------------

func TestBitcoinExtractTransfers_BatchPayment_MultipleRecipients(t *testing.T) {
	// Exchange-style batch payment: 1 input, multiple different recipient outputs,
	// plus a change output back to sender.
	const addrSender = "exchange_hot_wallet"
	const addrRecip1 = "customer_1"
	const addrRecip2 = "customer_2"
	const addrRecip3 = "customer_3"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: false,
	})

	tx := &bitcoin.Transaction{
		TxID: "batch_payment_tx",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, addrSender, 1.0)},
		Vout: []bitcoin.Output{
			btcOutput(addrRecip1, 0.1, 0),
			btcOutput(addrRecip2, 0.2, 1),
			btcOutput(addrRecip3, 0.3, 2),
			btcOutput(addrSender, 0.39, 3), // change
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 3, "three recipient outputs; change filtered")

	toAddresses := make(map[string]bool)
	for _, tr := range transfers {
		toAddresses[tr.ToAddress] = true
	}
	assert.True(t, toAddresses[addrRecip1])
	assert.True(t, toAddresses[addrRecip2])
	assert.True(t, toAddresses[addrRecip3])
	assert.False(t, toAddresses[addrSender], "change to sender must be filtered")

	// Fee attached to only first transfer
	feeCount := 0
	for _, tr := range transfers {
		if !tr.TxFee.IsZero() {
			feeCount++
		}
	}
	assert.Equal(t, 1, feeCount, "fee should be assigned to exactly one transfer")
}

// --- consolidation ----------------------------------------------------------

func TestBitcoinExtractTransfers_ConsolidationAllToSelf_ZeroTransfers(t *testing.T) {
	// A UTXO consolidation sweeps multiple inputs back to the same address.
	// With IndexChangeOutput=false, every output is classified as "change" and
	// filtered → zero Transfer events emitted (UTXO events still capture this).
	const addrOwner = "wallet_owner"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: false,
	})

	tx := &bitcoin.Transaction{
		TxID: "consolidation_tx",
		Vin: []bitcoin.Input{
			btcInput("prev1", 0, addrOwner, 0.1),
			btcInput("prev2", 1, addrOwner, 0.2),
			btcInput("prev3", 0, addrOwner, 0.15),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrOwner, 0.44, 0), // all back to owner
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	assert.Empty(t, transfers,
		"self-consolidation with IndexChangeOutput=false emits zero Transfer events "+
			"(UTXO stream still captures the spent/created UTXOs)")
}

func TestBitcoinExtractTransfers_ConsolidationAllToSelf_WithChangeOutputEnabled(t *testing.T) {
	const addrOwner = "wallet_owner"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true, // explicitly include self-transfers
	})

	tx := &bitcoin.Transaction{
		TxID: "consolidation_tx_v2",
		Vin: []bitcoin.Input{
			btcInput("prev1", 0, addrOwner, 0.3),
			btcInput("prev2", 0, addrOwner, 0.2),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrOwner, 0.49, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1, "with IndexChangeOutput=true the self-transfer is included")
	assert.Equal(t, addrOwner, transfers[0].FromAddress)
	assert.Equal(t, addrOwner, transfers[0].ToAddress)
}

// --- coinbase ---------------------------------------------------------------

func TestBitcoinExtractTransfers_Coinbase_ReturnsEmpty(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})

	coinbase := &bitcoin.Transaction{
		TxID: "coinbase_tx",
		Vin: []bitcoin.Input{
			// Coinbase: TxID is empty
			{Vout: 0xffffffff},
		},
		Vout: []bitcoin.Output{
			btcOutput("miner_address", 6.25, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(coinbase, 100, 1_000_000, 100)
	assert.Empty(t, transfers, "coinbase transactions must be skipped")
}

// --- OP_RETURN --------------------------------------------------------------

func TestBitcoinExtractTransfers_OPReturn_Skipped(t *testing.T) {
	const addrSender = "sender_alice"
	const addrRecipient = "recipient_bob"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "op_return_tx",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, addrSender, 0.5)},
		Vout: []bitcoin.Output{
			btcOutput(addrRecipient, 0.49, 0),
			btcOpReturnOutput(1), // data carrier, no address
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1, "OP_RETURN output must not generate a transfer")
	assert.Equal(t, addrRecipient, transfers[0].ToAddress)
}

// --- missing prevout --------------------------------------------------------

// KNOWN BUG #5 (partial prevout resolution):
// When Vin[0] has prevout data but a later input does not, the later input
// is skipped by both convertBlockWithPrevoutResolution and ResolvePrevouts
// (they only check Vin[0]). The result is an empty FromAddress for inputs
// that lack prevout data when they appear at index 1+.
func TestBitcoinExtractTransfers_NoPrevout_EmptyFromAddr(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	// All inputs lack PrevOut – fromAddr will be empty string
	tx := &bitcoin.Transaction{
		TxID: "no_prevout_tx",
		Vin: []bitcoin.Input{
			// No PrevOut on any input
			{TxID: "prev1", Vout: 0},
			{TxID: "prev2", Vout: 0},
		},
		Vout: []bitcoin.Output{
			btcOutput("recipient_bob", 0.49, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, "", transfers[0].FromAddress,
		"FromAddress must be empty when no input has prevout data")
}

// ─── extractUTXOEvent – unit tests ──────────────────────────────────────────

func TestBitcoinExtractUTXO_CapturesAllVoutsAndVins(t *testing.T) {
	// UTXO events capture every vout (created) and every vin with prevout
	// (spent), without address-based filtering. Filtering happens in the
	// worker's emitUTXOs when pubkeyStore is consulted.
	const addrA = "sender_alice"
	const addrB = "sender_bob"
	const addrC = "recipient_carol"
	const addrD = "recipient_dave"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId: "testnet3",
		IndexUTXO: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "utxo_test_tx",
		Vin: []bitcoin.Input{
			btcInput("prev_a", 0, addrA, 0.3),
			btcInput("prev_b", 1, addrB, 0.2),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrC, 0.24, 0),
			btcOutput(addrD, 0.24, 1),
			btcOpReturnOutput(2), // no address → must NOT appear in Created
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "blockhash123", 1_000_000, 100)

	require.NotNil(t, event)
	assert.Equal(t, tx.TxID, event.TxHash)

	// Created: only the 2 spendable outputs (OP_RETURN has no address → skipped)
	require.Len(t, event.Created, 2, "OP_RETURN output must not appear in Created")
	createdAddrs := make(map[string]bool)
	for _, u := range event.Created {
		createdAddrs[u.Address] = true
	}
	assert.True(t, createdAddrs[addrC])
	assert.True(t, createdAddrs[addrD])

	// Spent: all inputs with resolved PrevOut
	require.Len(t, event.Spent, 2)
	spentAddrs := make(map[string]bool)
	for _, s := range event.Spent {
		spentAddrs[s.Address] = true
	}
	assert.True(t, spentAddrs[addrA])
	assert.True(t, spentAddrs[addrB])
}

func TestBitcoinExtractUTXO_OPReturn_ExcludedFromCreated(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId: "testnet3",
		IndexUTXO: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "op_return_utxo",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, "sender", 0.5)},
		Vout: []bitcoin.Output{
			btcOutput("recipient", 0.49, 0),
			btcOpReturnOutput(1),
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "bh", 1_000_000, 100)

	require.NotNil(t, event)
	require.Len(t, event.Created, 1, "OP_RETURN must not appear in Created UTXOs")
	assert.Equal(t, "recipient", event.Created[0].Address)
}

func TestBitcoinExtractUTXO_Coinbase_ReturnsNil(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId: "testnet3",
		IndexUTXO: true,
	})

	coinbase := &bitcoin.Transaction{
		TxID: "coinbase",
		Vin:  []bitcoin.Input{{Vout: 0xffffffff}},
		Vout: []bitcoin.Output{btcOutput("miner", 6.25, 0)},
	}

	event := idx.extractUTXOEvent(coinbase, 100, "bh", 1_000_000, 100)
	assert.Nil(t, event, "coinbase transaction must return nil UTXOEvent")
}

func TestBitcoinExtractUTXO_MissingPrevout_InputExcludedFromSpent(t *testing.T) {
	// KNOWN BUG #5 (UTXO side-effect):
	// Inputs without prevout data are excluded from the Spent list because
	// GetInputAddress returns "" for them. Fee is also under-counted.
	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId: "testnet3",
		IndexUTXO: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "partial_prevout",
		Vin: []bitcoin.Input{
			btcInput("prev_a", 0, "addr_a", 0.5), // resolved
			{TxID: "prev_b", Vout: 0},             // NOT resolved – no PrevOut
		},
		Vout: []bitcoin.Output{
			btcOutput("recipient", 0.49, 0),
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "bh", 1_000_000, 100)

	require.NotNil(t, event)
	require.Len(t, event.Spent, 1,
		"KNOWN BUG #5: input without prevout is excluded from Spent UTXOs")
	assert.Equal(t, "addr_a", event.Spent[0].Address)
}

func TestBitcoinExtractUTXO_UTXO_Key_Format(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId: "testnet3",
		IndexUTXO: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "key_test_tx",
		Vin:  []bitcoin.Input{btcInput("prev1", 2, "sender", 1.0)},
		Vout: []bitcoin.Output{
			btcOutput("recip_a", 0.4, 0),
			btcOutput("recip_b", 0.59, 1),
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "bh", 1_000_000, 100)
	require.NotNil(t, event)

	// Created UTXO key = txHash:vout
	assert.Equal(t, "key_test_tx:0", event.Created[0].Key())
	assert.Equal(t, "key_test_tx:1", event.Created[1].Key())

	// Spent UTXO key = prevTxHash:prevVout
	assert.Equal(t, "prev1:2", event.Spent[0].Key())
}

// ─── address normalization ──────────────────────────────────────────────────

func TestBitcoinNormalize_P2WPKH_Lowercase(t *testing.T) {
	// Native SegWit bech32 (bc1q) is normalised to lowercase.
	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	got, err := bitcoin.NormalizeBTCAddress(addr)
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(addr), got)
}

func TestBitcoinNormalize_TaprootP2TR_Bech32m(t *testing.T) {
	// KNOWN BUG #4:
	// github.com/btcsuite/btcutil/bech32 v1.0.2 only supports BIP-173 (bech32).
	// bc1p… addresses use BIP-350 (bech32m) encoding and cause bech32.Decode
	// to return an error. NormalizeBTCAddress therefore returns ("", error).
	//
	// Impact: extractTransfersFromTx gracefully falls back to the raw (already
	// lowercase) address, so Taproot transfers are NOT lost. However, callers
	// that rely on NormalizeBTCAddress to validate P2TR addresses receive a
	// false error for perfectly valid addresses.
	taprootAddr := "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"
	_, err := bitcoin.NormalizeBTCAddress(taprootAddr)

	// Document the current behaviour.  If the library is upgraded to support
	// bech32m, this assertion will fail – which is the desired signal to remove
	// this workaround.
	if err != nil {
		t.Logf("CONFIRMED BUG #4: NormalizeBTCAddress returns error for P2TR address: %v", err)
		t.Logf("Taproot address is still used raw (fallback) in extractTransfersFromTx.")
	} else {
		t.Log("bech32m is supported – KNOWN BUG #4 is resolved")
	}
	// No hard assertion; we just document the current state.
}

func TestBitcoinNormalize_TaprootFallback_TransferNotMissed(t *testing.T) {
	// Even when NormalizeBTCAddress fails for a P2TR address, the transfer
	// is still produced because extractTransfersFromTx uses the raw address
	// as a fallback.
	const addrSender = "sender_alice"
	taprootRecipient := "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	tx := &bitcoin.Transaction{
		TxID: "taproot_tx",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, addrSender, 0.5)},
		Vout: []bitcoin.Output{
			// Output directly uses the bc1p address string
			{
				Value: 0.49,
				N:     0,
				ScriptPubKey: bitcoin.ScriptPubKey{
					Address: taprootRecipient,
				},
			},
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)

	require.Len(t, transfers, 1, "transfer to Taproot address must NOT be lost")
	// The address in the transfer is the raw value returned by the RPC
	// (typically lowercase), not a normalized form.
	assert.Equal(t, taprootRecipient, transfers[0].ToAddress)
}

func TestBitcoinGetOutputAddress_BareMultisig_OnlyFirstAddress(t *testing.T) {
	// KNOWN BUG #3 (low-level):
	// GetOutputAddress returns only Addresses[0] when ScriptPubKey.Address
	// is empty (bare multisig case).
	output := &bitcoin.Output{
		Value: 1.0,
		ScriptPubKey: bitcoin.ScriptPubKey{
			Type:      "multisig",
			Addresses: []string{"addr_first", "addr_second", "addr_third"},
		},
	}

	got := bitcoin.GetOutputAddress(output)
	assert.Equal(t, "addr_first", got,
		"GetOutputAddress returns only the first address for bare multisig")
	// addr_second and addr_third are invisible – see KNOWN BUG #3 above.
}

// ─── status / confirmations ─────────────────────────────────────────────────

func TestBitcoinExtractTransfers_ConfirmationStatus(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})

	tx := &bitcoin.Transaction{
		TxID: "conf_test",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, "sender", 0.5)},
		Vout: []bitcoin.Output{btcOutput("recipient", 0.49, 0)},
	}

	// Block 100, latest block 100 → 1 confirmation → confirmed
	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)
	require.Len(t, transfers, 1)
	assert.Equal(t, uint64(1), transfers[0].Confirmations)
	assert.Equal(t, "confirmed", transfers[0].Status)

	// Mempool (blockNumber=0) → 0 confirmations → pending
	transfers = idx.extractTransfersFromTx(tx, 0, 1_000_000, 100)
	require.Len(t, transfers, 1)
	assert.Equal(t, uint64(0), transfers[0].Confirmations)
	assert.Equal(t, "pending", transfers[0].Status)
}

func TestBitcoinExtractTransfers_Amount_Satoshis(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})

	tx := &bitcoin.Transaction{
		TxID: "amount_test",
		Vin:  []bitcoin.Input{btcInput("prev1", 0, "sender", 0.5)},
		Vout: []bitcoin.Output{
			btcOutput("recipient", 0.1, 0), // 0.1 BTC = 10_000_000 sat
		},
	}

	transfers := idx.extractTransfersFromTx(tx, 100, 1_000_000, 100)
	require.Len(t, transfers, 1)
	assert.Equal(t, "10000000", transfers[0].Amount, "0.1 BTC must be 10000000 satoshis (no float truncation)")
}

// ─── integration tests (require network, skipped with -short) ───────────────
//
// These tests fetch real data from the Bitcoin testnet (testnet3) and run the
// extraction logic against it to confirm behaviour against actual on-chain
// transactions.

const btcTestnetRPCURL = "https://bitcoin-testnet-rpc.publicnode.com"

// testnet3 block with known diverse transaction types.
// Block 4842314 is already used in bitcoin_verbosity_test.go.
const btcIntegrationBlock = uint64(4842314)

func newBTCTestClient(t *testing.T) *bitcoin.BitcoinClient {
	t.Helper()
	return bitcoin.NewBitcoinClient(btcTestnetRPCURL, nil, 60*time.Second, nil)
}

func TestBitcoinExtract_Integration_MultiInputTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	block, err := client.GetBlockByHeight(ctx, btcIntegrationBlock, 3)
	require.NoError(t, err, "failed to fetch testnet block")

	// Find first non-coinbase transaction with 2+ inputs where all inputs
	// have prevout data (verbosity=3 should supply this).
	var multiInputTx *bitcoin.Transaction
	for i := range block.Tx {
		tx := &block.Tx[i]
		if tx.IsCoinbase() || len(tx.Vin) < 2 {
			continue
		}
		allResolved := true
		for _, vin := range tx.Vin {
			if vin.PrevOut == nil {
				allResolved = false
				break
			}
		}
		if allResolved {
			multiInputTx = tx
			break
		}
	}

	if multiInputTx == nil {
		t.Skipf("no multi-input tx with all prevouts resolved found in block %d", btcIntegrationBlock)
	}

	t.Logf("Using multi-input tx: %s (%d inputs, %d outputs)",
		multiInputTx.TxID, len(multiInputTx.Vin), len(multiInputTx.Vout))

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	transfers := idx.extractTransfersFromTx(multiInputTx, block.Height, block.Time, block.Height)

	// Must produce at least one transfer per non-OP_RETURN output.
	require.NotEmpty(t, transfers, "multi-input tx must produce at least one transfer")

	// ALL transfers must use the first input's address as FromAddress.
	expectedFrom := bitcoin.GetInputAddress(&multiInputTx.Vin[0])
	if expectedFrom != "" {
		// Normalize for comparison
		if normalized, err := bitcoin.NormalizeBTCAddress(expectedFrom); err == nil {
			expectedFrom = normalized
		}
		for _, tr := range transfers {
			assert.Equal(t, expectedFrom, tr.FromAddress,
				"all transfers from a multi-input tx must show fromAddr = first input's address (tx: %s)", multiInputTx.TxID)
		}
	}

	// No transfer should reference the second+ input addresses.
	if len(multiInputTx.Vin) >= 2 {
		secondInputAddr := bitcoin.GetInputAddress(&multiInputTx.Vin[1])
		if secondInputAddr != "" {
			if normalized, err := bitcoin.NormalizeBTCAddress(secondInputAddr); err == nil {
				secondInputAddr = normalized
			}
			if secondInputAddr != expectedFrom { // only test when they differ
				for _, tr := range transfers {
					assert.NotEqual(t, secondInputAddr, tr.FromAddress,
						"second input's address must never appear as FromAddress (BUG #1 confirmation)")
				}
			}
		}
	}
}

func TestBitcoinExtract_Integration_BatchPaymentTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	block, err := client.GetBlockByHeight(ctx, btcIntegrationBlock, 3)
	require.NoError(t, err)

	// Find first non-coinbase tx with 3+ outputs (batch payment candidate).
	var batchTx *bitcoin.Transaction
	for i := range block.Tx {
		tx := &block.Tx[i]
		if !tx.IsCoinbase() && len(tx.Vout) >= 3 {
			batchTx = tx
			break
		}
	}

	if batchTx == nil {
		t.Skipf("no batch payment tx found in block %d", btcIntegrationBlock)
	}

	t.Logf("Using batch payment tx: %s (%d inputs, %d outputs)",
		batchTx.TxID, len(batchTx.Vin), len(batchTx.Vout))

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true, // include all outputs for counting
	})

	transfers := idx.extractTransfersFromTx(batchTx, block.Height, block.Time, block.Height)

	// Count spendable outputs (those with an address)
	spendableOutputs := 0
	for i := range batchTx.Vout {
		if bitcoin.GetOutputAddress(&batchTx.Vout[i]) != "" {
			spendableOutputs++
		}
	}

	assert.Equal(t, spendableOutputs, len(transfers),
		"each spendable output must produce exactly one transfer (with IndexChangeOutput=true)")
}

func TestBitcoinExtract_Integration_CoinbaseNotInTransfers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	block, err := client.GetBlockByHeight(ctx, btcIntegrationBlock, 3)
	require.NoError(t, err)
	require.NotEmpty(t, block.Tx, "block must have transactions")

	// First transaction in every block is the coinbase.
	coinbase := &block.Tx[0]
	require.True(t, coinbase.IsCoinbase(), "first transaction must be coinbase")

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})

	transfers := idx.extractTransfersFromTx(coinbase, block.Height, block.Time, block.Height)
	assert.Empty(t, transfers, "coinbase transaction must produce zero transfers")
}

func TestBitcoinExtract_Integration_UTXOEventStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	block, err := client.GetBlockByHeight(ctx, btcIntegrationBlock, 3)
	require.NoError(t, err)

	// Pick the first non-coinbase transaction with at least one resolved prevout.
	var targetTx *bitcoin.Transaction
	for i := range block.Tx {
		tx := &block.Tx[i]
		if !tx.IsCoinbase() && len(tx.Vin) > 0 && tx.Vin[0].PrevOut != nil {
			targetTx = tx
			break
		}
	}
	require.NotNil(t, targetTx, "expected at least one non-coinbase tx with prevout in block")

	t.Logf("Using tx: %s (%d inputs, %d outputs)", targetTx.TxID, len(targetTx.Vin), len(targetTx.Vout))

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId: "testnet3",
		IndexUTXO: true,
	})

	event := idx.extractUTXOEvent(targetTx, block.Height, block.Hash, block.Time, block.Height)
	require.NotNil(t, event)

	assert.Equal(t, targetTx.TxID, event.TxHash)
	assert.Equal(t, block.Height, event.BlockNumber)
	assert.Equal(t, block.Hash, event.BlockHash)
	assert.Equal(t, "confirmed", event.Status, "block transaction must be confirmed")

	// Count spendable outputs
	spendableVouts := 0
	for i := range targetTx.Vout {
		if bitcoin.GetOutputAddress(&targetTx.Vout[i]) != "" {
			spendableVouts++
		}
	}
	assert.Equal(t, spendableVouts, len(event.Created),
		"Created UTXOs must match count of spendable vouts")

	// Count inputs with resolved prevout
	resolvedVins := 0
	for _, vin := range targetTx.Vin {
		if vin.PrevOut != nil && bitcoin.GetOutputAddress(vin.PrevOut) != "" {
			resolvedVins++
		}
	}
	assert.Equal(t, resolvedVins, len(event.Spent),
		"Spent UTXOs must match count of inputs with resolved prevout")

	// Each spent UTXO key must be "prevTxHash:prevVout"
	for _, spent := range event.Spent {
		assert.NotEmpty(t, spent.TxHash, "SpentUTXO.TxHash must not be empty")
		expectedKey := fmt.Sprintf("%s:%d", spent.TxHash, spent.Vout)
		assert.Equal(t, expectedKey, spent.Key())
	}

	// Fee must be non-negative
	fee, err := decimal.NewFromString(event.TxFee)
	require.NoError(t, err, "TxFee must be a valid decimal string")
	assert.False(t, fee.IsNegative(), "TxFee must be >= 0")
}

func TestBitcoinExtract_Integration_SpecificTxWithKnownOutputCount(t *testing.T) {
	// This test fetches a specific testnet transaction by hash and validates
	// the extraction logic against its known structure.
	//
	// To find a suitable tx for this test:
	//   1. Fetch block 4842314 from testnet
	//   2. Pick a non-coinbase tx with 2+ inputs and 2+ outputs
	//   3. Record its txid and expected transfer count
	//
	// Run with: go test -v -run TestBitcoinExtract_Integration_SpecificTxWithKnownOutputCount ./internal/indexer/
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	block, err := client.GetBlockByHeight(ctx, btcIntegrationBlock, 3)
	require.NoError(t, err)

	// Log all non-coinbase transactions so the test can be narrowed to a
	// specific txid with known expected values on subsequent runs.
	t.Logf("Block %d has %d transactions:", block.Height, len(block.Tx))
	for i, tx := range block.Tx {
		if tx.IsCoinbase() {
			t.Logf("  [%d] COINBASE %s", i, tx.TxID)
			continue
		}
		// Count spendable outputs
		spendable := 0
		for j := range tx.Vout {
			if bitcoin.GetOutputAddress(&tx.Vout[j]) != "" {
				spendable++
			}
		}
		prevoutResolved := 0
		for _, vin := range tx.Vin {
			if vin.PrevOut != nil {
				prevoutResolved++
			}
		}
		t.Logf("  [%d] txid=%s  inputs=%d(resolved=%d)  outputs=%d(spendable=%d)",
			i, tx.TxID,
			len(tx.Vin), prevoutResolved,
			len(tx.Vout), spendable,
		)
	}

	// Pick the first non-coinbase tx with 2+ inputs and 2+ outputs for a
	// concrete assertion. This also exercises the multi-input + multi-output path.
	var targetTx *bitcoin.Transaction
	for i := range block.Tx {
		tx := &block.Tx[i]
		if !tx.IsCoinbase() && len(tx.Vin) >= 2 && len(tx.Vout) >= 2 {
			targetTx = tx
			break
		}
	}

	if targetTx == nil {
		t.Skip("no 2-input 2-output tx in block – adjust btcIntegrationBlock")
	}

	t.Logf("Selected tx %s: %d inputs, %d outputs", targetTx.TxID, len(targetTx.Vin), len(targetTx.Vout))

	idx := newBTCTestIndexer(config.ChainConfig{
		NetworkId:         "testnet3",
		IndexChangeOutput: true,
	})
	transfers := idx.extractTransfersFromTx(targetTx, block.Height, block.Time, block.Height)

	spendableOutputs := 0
	for i := range targetTx.Vout {
		if bitcoin.GetOutputAddress(&targetTx.Vout[i]) != "" {
			spendableOutputs++
		}
	}

	assert.Equal(t, spendableOutputs, len(transfers),
		"tx %s: transfer count must equal spendable output count", targetTx.TxID)

	// Document: FromAddress == first input's address
	if targetTx.Vin[0].PrevOut != nil {
		firstInputAddr := bitcoin.GetInputAddress(&targetTx.Vin[0])
		if norm, err := bitcoin.NormalizeBTCAddress(firstInputAddr); err == nil {
			firstInputAddr = norm
		}
		for _, tr := range transfers {
			assert.Equal(t, firstInputAddr, tr.FromAddress,
				"FromAddress must be first input's address for all transfers")
		}
	}
}
