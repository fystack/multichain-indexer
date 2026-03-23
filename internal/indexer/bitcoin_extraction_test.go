package indexer

// bitcoin_extraction_test.go
//
// Unit and integration tests for Bitcoin transaction extraction.
//
// Real on-chain fixtures are sourced from testnet3 block 4842314:
//
//   txMultiInputConsolidation  4d21c6ef…  3 inputs (same address), 1 output
//   txBatchPaymentWithChange   d7ff8b64…  1 input, 3 outputs (incl. change back)
//   txStressMultiSender        5db87486…  8 inputs (2 unique senders), 3 outputs
//
// Integration tests (require network) are skipped with -short.

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

func newBTCTestIndexer(cfg config.ChainConfig) *BitcoinIndexer {
	return &BitcoinIndexer{chainName: "bitcoin_test", config: cfg}
}

// btcInput builds an Input with fully resolved PrevOut.
func btcInput(prevTxID string, prevVout uint32, addr string, valueBTC float64) bitcoin.Input {
	return bitcoin.Input{
		TxID: prevTxID,
		Vout: prevVout,
		PrevOut: &bitcoin.Output{
			Value:        valueBTC,
			ScriptPubKey: bitcoin.ScriptPubKey{Address: addr},
		},
	}
}

// btcOutput builds a standard single-address output.
func btcOutput(addr string, valueBTC float64, n uint32) bitcoin.Output {
	return bitcoin.Output{
		Value: valueBTC,
		N:     n,
		ScriptPubKey: bitcoin.ScriptPubKey{Address: addr},
	}
}

// btcOpReturnOutput builds an unspendable OP_RETURN output.
func btcOpReturnOutput(n uint32) bitcoin.Output {
	return bitcoin.Output{
		Value: 0,
		N:     n,
		ScriptPubKey: bitcoin.ScriptPubKey{
			Type: "nulldata",
			ASM:  "OP_RETURN 68656c6c6f",
			Hex:  "6a0568656c6c6f",
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
		},
	}
}

// ─── real on-chain fixtures (testnet3 block 4842314) ────────────────────────

const (
	// 3 inputs from the same address, 1 output — UTXO consolidation.
	txidMultiInputConsolidation = "4d21c6ef41187b2e62cf255bd517e4ad0e736bfd0fba305bf9a16cb9e9051b21"
	addrConsolidationSender     = "tb1qvv9nhsmxevwgfl5yujatm8v309r07v3a7wxlvc"
	addrConsolidationRecipient  = "tb1qgwve6632ppezvfneg930gvt5zleu0zwjdj53nv"

	// 1 input, 3 outputs: 2 payments + 1 change back to sender.
	txidBatchPaymentWithChange = "d7ff8b64dee9efce1a3452dc85134e65dff541da276c724bb744bd2d3df6df21"
	addrBatchSender            = "tb1qvnsryrfswcgy8sseu54uv29ph9xyl6gzln9yev"
	addrBatchRecipient1        = "tb1q3gh87y4w0vu3ekz5zlsdkyzy7ka7v0732q4nwn"
	addrBatchRecipient2        = "tb1qx8ujyagtygmv8rppmswutwnpm6ms6sw6d8y8f8"

	// 8 inputs (2 unique senders), 3 outputs.
	txidStressMultiSender = "5db8748682dffdf4b827a213691b533a4e45080a7ca3c2a6d761d06478c77627"
	addrStressSenderA     = "mjHWQNQnng4DxGHR9KZofwSkLYEsoRi67q" // P2PKH testnet, Vin[0]
	addrStressSenderB     = "mwJTRrr6xKyggw8kpdwFtf6ZTaAfGn5xJo" // P2PKH testnet, Vin[1-7]
	addrStressExternal    = "n4Wi7KMMvfAmYoHpyXvWkT9DEQcHEW6u2y" // external recipient

	btcTestnetRPCURL    = "https://bitcoin-testnet-rpc.publicnode.com"
	btcIntegrationBlock = uint64(4842314)
)

// fixtureMultiInputConsolidation returns the hardcoded fixture for
// txidMultiInputConsolidation (testnet3).
//
// 3 inputs from the same address (consolidation), 1 output.
// Total input: 23345 + 2091 + 15399 = 40835 sat
// Output: 21164 sat  |  Fee: 19671 sat
func fixtureMultiInputConsolidation() *bitcoin.Transaction {
	return &bitcoin.Transaction{
		TxID: txidMultiInputConsolidation,
		Vin: []bitcoin.Input{
			btcInput("6cda9af4d20f7e15a92c1addc575ebc6896e438d2d116ff29667845e79f9c7ee", 0,
				addrConsolidationSender, 0.00023345),
			btcInput("793bc523c086267dc7c7e41347dad6d071225470d12bb3be27084096127181e6", 1,
				addrConsolidationSender, 2.091e-05),
			btcInput("e4ea4c555fb17213e3608edfc741b2127289f6af77c233b9f7166d169f250b26", 0,
				addrConsolidationSender, 0.00015399),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrConsolidationRecipient, 0.00021164, 0),
		},
	}
}

// fixtureBatchPaymentWithChange returns the hardcoded fixture for
// txidBatchPaymentWithChange (testnet3).
//
// 1 input, 3 outputs: two payments + one change output back to sender.
// Input: 5374454 sat
// Output[0]: 20860 sat  Output[1]: 20831 sat  Output[2]: 5321583 sat (change)
// Fee: 11180 sat
func fixtureBatchPaymentWithChange() *bitcoin.Transaction {
	return &bitcoin.Transaction{
		TxID: txidBatchPaymentWithChange,
		Vin: []bitcoin.Input{
			btcInput("1595525125319977680523c2e9fd643da987190c27e1b57544aea3a2c2327f6f", 2,
				addrBatchSender, 0.05374454),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrBatchRecipient1, 0.0002086, 0),
			btcOutput(addrBatchRecipient2, 0.00020831, 1),
			btcOutput(addrBatchSender, 0.05321583, 2), // change back to sender
		},
	}
}

// fixtureStressMultiSender returns the hardcoded fixture for
// txidStressMultiSender (testnet3).
//
// 8 inputs: Vin[0] from addrStressSenderA (P2PKH), Vin[1-7] from addrStressSenderB.
// 3 outputs: external, senderB-change, senderA-change.
// Total input: 117215401 sat  |  Total output: 117213127 sat  |  Fee: 2274 sat
func fixtureStressMultiSender() *bitcoin.Transaction {
	return &bitcoin.Transaction{
		TxID: txidStressMultiSender,
		Vin: []bitcoin.Input{
			btcInput("eb5a699c070a4d5fc76821f29bc1988582b5861a7c68c58efce64b063d8c0cb0", 2,
				addrStressSenderA, 0.9999768),
			btcInput("52439581491036eff658ec6daac26c2d83a579750f14e84b5420117ceb419d6f", 1,
				addrStressSenderB, 0.05552521),
			btcInput("3f7be9fa9b6ef9ea90384aa1432505c244ff0eaa72a8251222d4880c0c227116", 0,
				addrStressSenderB, 0.01),
			btcInput("b79424a25a201732341aef63b908d0356b314ebd159ea70cf528766194dd9352", 1,
				addrStressSenderB, 0.003),
			btcInput("49bd625d168dffd79dc3e437d6059aa99a47f572c44518a2ab565a9370fccb2e", 0,
				addrStressSenderB, 0.001),
			btcInput("eb5a699c070a4d5fc76821f29bc1988582b5861a7c68c58efce64b063d8c0cb0", 1,
				addrStressSenderB, 0.002529),
			btcInput("8fde01395032d9a7f3eada1b20ded3658e6079cb8c0e13b21082003a23f20ec6", 0,
				addrStressSenderB, 0.000123),
			btcInput("c675c49968a03e1eee31ee0385f9671a73051dabe4935924a75c5893fca89554", 0,
				addrStressSenderB, 0.1),
		},
		Vout: []bitcoin.Output{
			btcOutput(addrStressExternal, 0.099, 0),
			btcOutput(addrStressSenderB, 0.07318111, 1), // change back to senderB
			btcOutput(addrStressSenderA, 0.99995016, 2), // change back to senderA
		},
	}
}

func newBTCTestClient(t *testing.T) *bitcoin.BitcoinClient {
	t.Helper()
	return bitcoin.NewBitcoinClient(btcTestnetRPCURL, nil, 60*time.Second, nil)
}

// ─── satoshisFromFloat precision ────────────────────────────────────────────

func TestBitcoinSatoshisFromFloat_Precision(t *testing.T) {
	tests := []struct {
		btc  float64
		want int64
	}{
		{0.00000001, 1},
		{0.1, 10_000_000},         // classic float64 truncation case: 0.1*1e8 = 9999999.999…
		{0.29300000, 29_300_000},  // another float64 hazard
		{1.0, 100_000_000},
		{2.091e-05, 2_091},        // real input from txidMultiInputConsolidation Vin[1]
		{0.00023345, 23_345},      // real input from txidMultiInputConsolidation Vin[0]
		{0.05374454, 5_374_454},   // real input from txidBatchPaymentWithChange
		{0.9999768, 99_997_680},   // real input from txidStressMultiSender Vin[0]
	}
	for _, tc := range tests {
		got := satoshisFromFloat(tc.btc)
		assert.Equal(t, tc.want, got,
			"satoshisFromFloat(%.8f) should be %d sat", tc.btc, tc.want)
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
		Vout: []bitcoin.Output{btcOutput("addr_D", 0.64, 0)},
	}
	want := decimal.RequireFromString("0.01")
	assert.True(t, want.Equal(tx.CalculateFee()), "fee: want %s got %s", want, tx.CalculateFee())
}

func TestBitcoinCalculateFee_NegativeClamped(t *testing.T) {
	tx := &bitcoin.Transaction{
		TxID: "neg_fee",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "addr_A", 0.1)},
		Vout: []bitcoin.Output{btcOutput("addr_B", 0.5, 0)},
	}
	assert.True(t, decimal.Zero.Equal(tx.CalculateFee()))
}

func TestBitcoinCalculateFee_MissingPrevout_SkipsInput(t *testing.T) {
	// Inputs without prevout don't contribute to fee calculation.
	tx := &bitcoin.Transaction{
		TxID: "missing_prevout",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "addr_A", 0.5),
			{TxID: "p2", Vout: 0}, // no PrevOut
		},
		Vout: []bitcoin.Output{btcOutput("addr_B", 0.59, 0)},
	}
	// 0.5 (only resolved input) - 0.59 = negative → clamped to 0
	assert.True(t, decimal.Zero.Equal(tx.CalculateFee()))
}

// TestBitcoinCalculateFee_RealConsolidation verifies fee against the known
// real on-chain consolidation transaction (testnet3).
func TestBitcoinCalculateFee_RealConsolidation(t *testing.T) {
	tx := fixtureMultiInputConsolidation()
	fee := tx.CalculateFee()
	// 40835 - 21164 = 19671 sat = 0.00019671 BTC
	want := decimal.RequireFromString("0.00019671")
	assert.True(t, want.Equal(fee), "fee: want %s got %s", want, fee)
}

func TestBitcoinCalculateFee_RealBatchPayment(t *testing.T) {
	tx := fixtureBatchPaymentWithChange()
	fee := tx.CalculateFee()
	// 5374454 - 5363274 = 11180 sat = 0.0001118 BTC
	want := decimal.RequireFromString("0.0001118")
	assert.True(t, want.Equal(fee), "fee: want %s got %s", want, fee)
}

func TestBitcoinCalculateFee_RealStressMultiSender(t *testing.T) {
	tx := fixtureStressMultiSender()
	fee := tx.CalculateFee()
	// 117215401 - 117213127 = 2274 sat = 0.00002274 BTC
	want := decimal.RequireFromString("0.00002274")
	assert.True(t, want.Equal(fee), "fee: want %s got %s", want, fee)
}

// ─── getAllInputAddresses ────────────────────────────────────────────────────

func TestBitcoinGetAllInputAddresses_SingleInput(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "single",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "addr_a", 0.5)},
	}
	assert.Equal(t, []string{"addr_a"}, idx.getAllInputAddresses(tx))
}

func TestBitcoinGetAllInputAddresses_Deduplicated(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "dedup",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "addr_a", 0.3),
			btcInput("p2", 1, "addr_a", 0.2), // same address
			btcInput("p3", 0, "addr_b", 0.1),
		},
	}
	// addr_a appears twice but must be deduplicated; order of first appearance preserved
	assert.Equal(t, []string{"addr_a", "addr_b"}, idx.getAllInputAddresses(tx))
}

func TestBitcoinGetAllInputAddresses_MissingPrevoutSkipped(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "partial",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "addr_a", 1.0),
			{TxID: "p2", Vout: 0}, // no PrevOut
			btcInput("p3", 0, "addr_c", 0.5),
		},
	}
	assert.Equal(t, []string{"addr_a", "addr_c"}, idx.getAllInputAddresses(tx))
}

func TestBitcoinGetAllInputAddresses_NoPrevout(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "no_prevout",
		Vin:  []bitcoin.Input{{TxID: "p1"}, {TxID: "p2"}},
	}
	assert.Empty(t, idx.getAllInputAddresses(tx))
}

// TestBitcoinGetAllInputAddresses_RealConsolidation verifies that three inputs
// from the same address deduplicate to a single entry.
func TestBitcoinGetAllInputAddresses_RealConsolidation(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := fixtureMultiInputConsolidation()

	addrs := idx.getAllInputAddresses(tx)

	require.Len(t, addrs, 1, "three inputs from the same address must deduplicate to one")
	assert.Equal(t, addrConsolidationSender, addrs[0])
}

// TestBitcoinGetAllInputAddresses_RealStressCase verifies that eight inputs from
// two distinct addresses produce exactly two deduplicated entries.
func TestBitcoinGetAllInputAddresses_RealStressCase(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := fixtureStressMultiSender()

	addrs := idx.getAllInputAddresses(tx)

	require.Len(t, addrs, 2, "8 inputs from 2 distinct addresses must deduplicate to 2")
	assert.Equal(t, addrStressSenderA, addrs[0], "senderA must be first (it owns Vin[0])")
	assert.Equal(t, addrStressSenderB, addrs[1], "senderB must be second")
}

// ─── extractTransfersFromTx ──────────────────────────────────────────────────

func TestBitcoinExtractTransfers_SingleInputSingleOutput(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "simple",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender_alice", 0.5)},
		Vout: []bitcoin.Output{btcOutput("recipient_bob", 0.49, 0)},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, "sender_alice", transfers[0].FromAddress)
	assert.Equal(t, []string{"sender_alice"}, transfers[0].FromAddresses)
	assert.Equal(t, "recipient_bob", transfers[0].ToAddress)
	assert.Equal(t, "49000000", transfers[0].Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, transfers[0].Type)
	assert.False(t, transfers[0].TxFee.IsZero())
}

// TestBitcoinExtractTransfers_OutputToSenderAddressIsEmitted verifies Bug #2 fix:
// an output whose address also appears in the input set is now emitted, not
// silently dropped as "change".
func TestBitcoinExtractTransfers_OutputToSenderAddressIsEmitted(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "input_also_receives",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "sender_alice", 0.5),
			btcInput("p2", 0, "sender_bob", 0.1),
		},
		Vout: []bitcoin.Output{
			btcOutput("sender_bob", 0.45, 0),  // net payment to bob (even though bob is also a sender)
			btcOutput("recipient_carol", 0.14, 1),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 2, "both outputs must be emitted; the output to sender_bob is a net payment, not change")
	toAddrs := map[string]bool{transfers[0].ToAddress: true, transfers[1].ToAddress: true}
	assert.True(t, toAddrs["sender_bob"], "output to sender_bob must be emitted")
	assert.True(t, toAddrs["recipient_carol"])
}

// TestBitcoinExtractTransfers_MultiInput_FromAddresses verifies Bug #1 fix:
// FromAddresses contains all distinct input addresses; FromAddress is the first.
func TestBitcoinExtractTransfers_MultiInput_FromAddresses(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "multi_input",
		Vin: []bitcoin.Input{
			btcInput("p1", 0, "sender_first", 0.3),
			btcInput("p2", 0, "sender_second", 0.2),
			btcInput("p3", 0, "sender_third", 0.1),
		},
		Vout: []bitcoin.Output{btcOutput("recipient", 0.59, 0)},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, "sender_first", transfers[0].FromAddress,
		"FromAddress must always be the first input's address (backward compat)")
	assert.Equal(t, []string{"sender_first", "sender_second", "sender_third"},
		transfers[0].FromAddresses,
		"FromAddresses must contain all distinct senders")
}

// TestBitcoinExtractTransfers_MultisigOutput_AllAddresses verifies Bug #3 fix:
// a bare multisig output now emits one transfer per participant address.
func TestBitcoinExtractTransfers_MultisigOutput_AllAddresses(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "bare_multisig",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender_alice", 1.0)},
		Vout: []bitcoin.Output{
			btcMultisigOutput([]string{"ms_addr0", "ms_addr1", "ms_addr2"}, 0.99, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 3, "one transfer per multisig participant address")
	toAddrs := map[string]bool{}
	for _, tr := range transfers {
		toAddrs[tr.ToAddress] = true
	}
	assert.True(t, toAddrs["ms_addr0"])
	assert.True(t, toAddrs["ms_addr1"])
	assert.True(t, toAddrs["ms_addr2"])
}

func TestBitcoinExtractTransfers_FeeOnFirstOutputOnly(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "multi_out",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 1.0)},
		Vout: []bitcoin.Output{
			btcOutput("recip_a", 0.4, 0),
			btcOutput("recip_b", 0.5, 1),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 2)
	assert.True(t, transfers[0].TxFee.IsPositive(), "fee attached to first transfer")
	assert.True(t, transfers[1].TxFee.IsZero(), "subsequent transfers have zero fee")
}

func TestBitcoinExtractTransfers_Coinbase_ReturnsEmpty(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	coinbase := &bitcoin.Transaction{
		TxID: "coinbase",
		Vin:  []bitcoin.Input{{Vout: 0xffffffff}},
		Vout: []bitcoin.Output{btcOutput("miner", 3.125, 0)},
	}
	assert.Empty(t, idx.extractTransfersFromTx(coinbase, "testhash", 100, 1_000_000, 100))
}

func TestBitcoinExtractTransfers_OPReturn_Skipped(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "op_return",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 0.5)},
		Vout: []bitcoin.Output{
			btcOutput("recipient", 0.49, 0),
			btcOpReturnOutput(1),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, "recipient", transfers[0].ToAddress)
}

func TestBitcoinExtractTransfers_ConfirmationStatus(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "conf_test",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 0.5)},
		Vout: []bitcoin.Output{btcOutput("recipient", 0.49, 0)},
	}

	// 1 confirmation
	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)
	require.Len(t, transfers, 1)
	assert.Equal(t, uint64(1), transfers[0].Confirmations)
	assert.Equal(t, "confirmed", transfers[0].Status)

	// mempool (blockNumber=0)
	transfers = idx.extractTransfersFromTx(tx, "", 0, 1_000_000, 100)
	require.Len(t, transfers, 1)
	assert.Equal(t, uint64(0), transfers[0].Confirmations)
	assert.Equal(t, "pending", transfers[0].Status)
}

func TestBitcoinExtractTransfers_Amount_Satoshis(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "sat_test",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 0.5)},
		Vout: []bitcoin.Output{btcOutput("recipient", 0.1, 0)},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)
	require.Len(t, transfers, 1)
	assert.Equal(t, "10000000", transfers[0].Amount, "0.1 BTC = 10000000 sat (no float truncation)")
}

func TestBitcoinExtractTransfers_NoPrevout_EmptyFromAddr(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "no_prevout",
		Vin:  []bitcoin.Input{{TxID: "p1"}, {TxID: "p2"}},
		Vout: []bitcoin.Output{btcOutput("recipient", 0.49, 0)},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 1)
	assert.Equal(t, "", transfers[0].FromAddress, "no prevout → empty FromAddress")
	assert.Empty(t, transfers[0].FromAddresses)
}

func TestBitcoinExtractTransfers_BlockHashAndTransferIndex(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "dedup_test",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 1.0)},
		Vout: []bitcoin.Output{
			btcOutput("recip_a", 0.3, 0),
			btcOutput("recip_b", 0.3, 1),
			btcOutput("recip_a", 0.39, 2), // same address as vout 0 — would collide without TransferIndex
		},
	}

	transfers := idx.extractTransfersFromTx(tx, "blockhash_abc", 100, 1_000_000, 100)

	require.Len(t, transfers, 3)

	// All transfers must carry the block hash
	for _, tr := range transfers {
		assert.Equal(t, "blockhash_abc", tr.BlockHash)
	}

	// TransferIndex must be unique across all transfers (voutIdx:addrIdx)
	assert.Equal(t, "0:0", transfers[0].TransferIndex)
	assert.Equal(t, "1:0", transfers[1].TransferIndex)
	assert.Equal(t, "2:0", transfers[2].TransferIndex)

	// NATS Hash() must be unique even for same-address outputs
	hashes := map[string]bool{}
	for _, tr := range transfers {
		h := tr.Hash()
		assert.False(t, hashes[h], "Hash() collision: %s", h)
		hashes[h] = true
	}
}

func TestBitcoinExtractTransfers_MultisigTransferIndex(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "multisig_index",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 1.0)},
		Vout: []bitcoin.Output{
			btcMultisigOutput([]string{"ms_a", "ms_b"}, 0.99, 0),
		},
	}

	transfers := idx.extractTransfersFromTx(tx, "blockhash_xyz", 100, 1_000_000, 100)

	require.Len(t, transfers, 2)
	assert.Equal(t, "0:0", transfers[0].TransferIndex)
	assert.Equal(t, "0:1", transfers[1].TransferIndex)

	hashes := map[string]bool{}
	for _, tr := range transfers {
		h := tr.Hash()
		assert.False(t, hashes[h], "Hash() collision: %s", h)
		hashes[h] = true
	}
}

// ─── real-fixture transfer tests ─────────────────────────────────────────────

// TestBitcoinExtractTransfers_RealConsolidation uses the hardcoded fixture for
// txidMultiInputConsolidation (3 same-address inputs, 1 output).
func TestBitcoinExtractTransfers_RealConsolidation(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := fixtureMultiInputConsolidation()

	transfers := idx.extractTransfersFromTx(tx, "testhash", btcIntegrationBlock, 1_000_000, btcIntegrationBlock)

	require.Len(t, transfers, 1)

	tr := transfers[0]
	assert.Equal(t, txidMultiInputConsolidation, tr.TxHash)
	assert.Equal(t, addrConsolidationSender, tr.FromAddress)
	assert.Equal(t, []string{addrConsolidationSender}, tr.FromAddresses,
		"three inputs from the same address deduplicate to one entry in FromAddresses")
	assert.Equal(t, addrConsolidationRecipient, tr.ToAddress)
	assert.Equal(t, "21164", tr.Amount, "0.00021164 BTC = 21164 sat")
	assert.False(t, tr.TxFee.IsZero())
}

// TestBitcoinExtractTransfers_RealBatchPayment uses the hardcoded fixture for
// txidBatchPaymentWithChange (1 input, 3 outputs, change back to sender).
//
// This test confirms Bug #2 is fixed: the change output back to the sender
// (output[2]) is now emitted, not silently dropped.
func TestBitcoinExtractTransfers_RealBatchPayment(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := fixtureBatchPaymentWithChange()

	transfers := idx.extractTransfersFromTx(tx, "testhash", btcIntegrationBlock, 1_000_000, btcIntegrationBlock)

	require.Len(t, transfers, 3,
		"all 3 outputs must be emitted; the change output back to sender is no longer filtered")

	toAmounts := map[string]string{}
	for _, tr := range transfers {
		assert.Equal(t, addrBatchSender, tr.FromAddress)
		assert.Equal(t, []string{addrBatchSender}, tr.FromAddresses)
		toAmounts[tr.ToAddress] = tr.Amount
	}

	assert.Equal(t, "20860", toAmounts[addrBatchRecipient1])
	assert.Equal(t, "20831", toAmounts[addrBatchRecipient2])
	assert.Equal(t, "5321583", toAmounts[addrBatchSender],
		"change output back to sender must be emitted with correct amount")

	// Fee attached to the first output only
	feeCount := 0
	for _, tr := range transfers {
		if !tr.TxFee.IsZero() {
			feeCount++
		}
	}
	assert.Equal(t, 1, feeCount)
}

// TestBitcoinExtractTransfers_RealStressMultiSender uses the hardcoded fixture for
// txidStressMultiSender (8 inputs / 2 unique senders, 3 outputs).
//
// This test confirms Bug #1 fix: FromAddresses contains both senders even
// though there are 8 inputs. It also confirms Bug #2 fix: outputs back to
// sender addresses are emitted.
func TestBitcoinExtractTransfers_RealStressMultiSender(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := fixtureStressMultiSender()

	transfers := idx.extractTransfersFromTx(tx, "testhash", btcIntegrationBlock, 1_000_000, btcIntegrationBlock)

	require.Len(t, transfers, 3, "all 3 outputs must be emitted")

	for _, tr := range transfers {
		// Bug #1 fix: FromAddress is still the first input's address (backward compat)
		assert.Equal(t, addrStressSenderA, tr.FromAddress,
			"FromAddress must be first input's address (%s)", addrStressSenderA)
		// Bug #1 fix: FromAddresses contains both unique senders
		assert.Equal(t, []string{addrStressSenderA, addrStressSenderB}, tr.FromAddresses)
	}

	toAmounts := map[string]string{}
	for _, tr := range transfers {
		toAmounts[tr.ToAddress] = tr.Amount
	}
	assert.Equal(t, "9900000", toAmounts[addrStressExternal])
	assert.Equal(t, "7318111", toAmounts[addrStressSenderB],
		"change output to senderB must be emitted (Bug #2 fix)")
	assert.Equal(t, "99995016", toAmounts[addrStressSenderA],
		"change output to senderA must be emitted (Bug #2 fix)")
}

// ─── extractUTXOEvent ────────────────────────────────────────────────────────

func TestBitcoinExtractUTXO_CapturesAllVoutsAndVins(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	tx := &bitcoin.Transaction{
		TxID: "utxo_test",
		Vin: []bitcoin.Input{
			btcInput("prev_a", 0, "addr_a", 0.3),
			btcInput("prev_b", 1, "addr_b", 0.2),
		},
		Vout: []bitcoin.Output{
			btcOutput("addr_c", 0.24, 0),
			btcOutput("addr_d", 0.24, 1),
			btcOpReturnOutput(2), // must NOT appear in Created
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "blockhash123", 1_000_000, 100)

	require.NotNil(t, event)
	assert.Equal(t, tx.TxID, event.TxHash)

	require.Len(t, event.Created, 2, "OP_RETURN output must not appear in Created")
	createdAddrs := map[string]bool{}
	for _, u := range event.Created {
		createdAddrs[u.Address] = true
	}
	assert.True(t, createdAddrs["addr_c"])
	assert.True(t, createdAddrs["addr_d"])

	require.Len(t, event.Spent, 2)
	spentAddrs := map[string]bool{}
	for _, s := range event.Spent {
		spentAddrs[s.Address] = true
	}
	assert.True(t, spentAddrs["addr_a"])
	assert.True(t, spentAddrs["addr_b"])
}

// TestBitcoinExtractUTXO_MultisigOutput_OneEntryPerAddress verifies Bug #3 fix
// for the UTXO stream: a bare multisig output produces one UTXO entry per
// participant address, all sharing the same Vout index.
func TestBitcoinExtractUTXO_MultisigOutput_OneEntryPerAddress(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	tx := &bitcoin.Transaction{
		TxID: "multisig_utxo",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender", 3.0)},
		Vout: []bitcoin.Output{
			btcMultisigOutput([]string{"ms1", "ms2"}, 2.9, 0),
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "bh", 1_000_000, 100)

	require.NotNil(t, event)
	require.Len(t, event.Created, 2, "one UTXO entry per multisig address")
	utxoAddrs := map[string]bool{}
	for _, u := range event.Created {
		utxoAddrs[u.Address] = true
		assert.Equal(t, uint32(0), u.Vout, "all entries share the same Vout index")
	}
	assert.True(t, utxoAddrs["ms1"])
	assert.True(t, utxoAddrs["ms2"])
}

func TestBitcoinExtractUTXO_Coinbase_ReturnsNil(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	coinbase := &bitcoin.Transaction{
		TxID: "coinbase",
		Vin:  []bitcoin.Input{{Vout: 0xffffffff}},
		Vout: []bitcoin.Output{btcOutput("miner", 6.25, 0)},
	}
	assert.Nil(t, idx.extractUTXOEvent(coinbase, 100, "bh", 1_000_000, 100))
}

func TestBitcoinExtractUTXO_MissingPrevout_ExcludedFromSpent(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	tx := &bitcoin.Transaction{
		TxID: "partial_prevout",
		Vin: []bitcoin.Input{
			btcInput("prev_a", 0, "addr_a", 0.5),
			{TxID: "prev_b", Vout: 0}, // no PrevOut
		},
		Vout: []bitcoin.Output{btcOutput("recipient", 0.49, 0)},
	}

	event := idx.extractUTXOEvent(tx, 100, "bh", 1_000_000, 100)

	require.NotNil(t, event)
	require.Len(t, event.Spent, 1, "input without prevout is excluded from Spent")
	assert.Equal(t, "addr_a", event.Spent[0].Address)
}

func TestBitcoinExtractUTXO_KeyFormat(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	tx := &bitcoin.Transaction{
		TxID: "key_test",
		Vin:  []bitcoin.Input{btcInput("prevtx", 2, "sender", 1.0)},
		Vout: []bitcoin.Output{
			btcOutput("recip_a", 0.4, 0),
			btcOutput("recip_b", 0.59, 1),
		},
	}

	event := idx.extractUTXOEvent(tx, 100, "bh", 1_000_000, 100)
	require.NotNil(t, event)

	assert.Equal(t, "key_test:0", event.Created[0].Key())
	assert.Equal(t, "key_test:1", event.Created[1].Key())
	assert.Equal(t, "prevtx:2", event.Spent[0].Key())
}

// TestBitcoinExtractUTXO_RealConsolidation runs the UTXO extractor against the
// real consolidation fixture and verifies created/spent counts and amounts.
func TestBitcoinExtractUTXO_RealConsolidation(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	tx := fixtureMultiInputConsolidation()

	event := idx.extractUTXOEvent(tx, btcIntegrationBlock, "test_block_hash", 1_000_000, btcIntegrationBlock)

	require.NotNil(t, event)
	assert.Equal(t, txidMultiInputConsolidation, event.TxHash)

	require.Len(t, event.Created, 1)
	assert.Equal(t, addrConsolidationRecipient, event.Created[0].Address)
	assert.Equal(t, "21164", event.Created[0].Amount)

	require.Len(t, event.Spent, 3, "all three inputs must appear in Spent")
	for _, s := range event.Spent {
		assert.Equal(t, addrConsolidationSender, s.Address)
	}
	// Amounts in first-appearance order: 23345, 2091, 15399
	assert.Equal(t, "23345", event.Spent[0].Amount)
	assert.Equal(t, "2091", event.Spent[1].Amount)
	assert.Equal(t, "15399", event.Spent[2].Amount)
}

// ─── address normalization ────────────────────────────────────────────────────

func TestBitcoinNormalize_P2WPKH_Lowercase(t *testing.T) {
	addr := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
	got, err := bitcoin.NormalizeBTCAddress(addr)
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(addr), got)
}

// TestBitcoinNormalize_P2TR_Accepted verifies Bug #4 fix: P2TR (bc1p/tb1p)
// addresses are now accepted by NormalizeBTCAddress without error.
func TestBitcoinNormalize_P2TR_Mainnet(t *testing.T) {
	addr := "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr"
	got, err := bitcoin.NormalizeBTCAddress(addr)
	require.NoError(t, err, "P2TR mainnet address must be accepted (Bug #4 fix)")
	assert.Equal(t, addr, got)
}

func TestBitcoinNormalize_P2TR_Testnet(t *testing.T) {
	addr := "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"
	got, err := bitcoin.NormalizeBTCAddress(addr)
	require.NoError(t, err, "P2TR testnet address must be accepted (Bug #4 fix)")
	assert.Equal(t, addr, got)
}

func TestBitcoinNormalize_P2TR_NormalizesUppercase(t *testing.T) {
	upper := "BC1P5CYXNUXMEUWUVKWFEM96LQZSZD02N6XDCJRS20CAC6YQJJWUDPXQKEDRCR"
	got, err := bitcoin.NormalizeBTCAddress(upper)
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(upper), got)
}

func TestBitcoinNormalize_TaprootFallback_TransferNotMissed(t *testing.T) {
	// Even when using a P2TR recipient, the transfer is produced correctly.
	taprootRecipient := "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	tx := &bitcoin.Transaction{
		TxID: "taproot_tx",
		Vin:  []bitcoin.Input{btcInput("p1", 0, "sender_alice", 0.5)},
		Vout: []bitcoin.Output{{
			Value:        0.49,
			ScriptPubKey: bitcoin.ScriptPubKey{Address: taprootRecipient},
		}},
	}

	transfers := idx.extractTransfersFromTx(tx, "testhash", 100, 1_000_000, 100)

	require.Len(t, transfers, 1, "transfer to P2TR address must not be lost")
	assert.Equal(t, taprootRecipient, transfers[0].ToAddress)
}

func TestBitcoinGetOutputAddress_SingleAddress(t *testing.T) {
	out := &bitcoin.Output{ScriptPubKey: bitcoin.ScriptPubKey{Address: "addr_primary"}}
	assert.Equal(t, "addr_primary", bitcoin.GetOutputAddress(out))
}

// TestBitcoinGetOutputAddresses_AllParticipants verifies Bug #3 fix:
// GetOutputAddresses returns all addresses in a multisig output.
func TestBitcoinGetOutputAddresses_AllParticipants(t *testing.T) {
	out := &bitcoin.Output{
		ScriptPubKey: bitcoin.ScriptPubKey{
			Type:      "multisig",
			Addresses: []string{"ms_addr0", "ms_addr1", "ms_addr2"},
		},
	}
	assert.Equal(t, []string{"ms_addr0", "ms_addr1", "ms_addr2"}, bitcoin.GetOutputAddresses(out))
}

// ─── convertBlockWithPrevoutResolution ───────────────────────────────────────

// TestBitcoinConvertBlock_AllPrevoutsResolved verifies that when all inputs
// already have prevout data (no resolution needed), all senders appear in
// FromAddresses.
func TestBitcoinConvertBlock_AllPrevoutsResolved_AllSendersPresent(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	block := &bitcoin.Block{
		Height:        100,
		Confirmations: 1,
		Hash:          "blockhash",
		Tx: []bitcoin.Transaction{
			{
				TxID: "tx1",
				Vin: []bitcoin.Input{
					btcInput("p1", 0, "sender_a", 2.0),
					btcInput("p2", 0, "sender_b", 1.0),
				},
				Vout: []bitcoin.Output{btcOutput("recipient", 2.9, 0)},
			},
		},
	}

	result, err := idx.convertBlockWithPrevoutResolution(context.Background(), block)

	require.NoError(t, err)
	require.Len(t, result.Transactions, 1)
	assert.Equal(t, "sender_a", result.Transactions[0].FromAddress)
	assert.Equal(t, []string{"sender_a", "sender_b"}, result.Transactions[0].FromAddresses)
}

// TestBitcoinConvertBlock_RealConsolidationFixture runs convertBlockWithPrevoutResolution
// against the real multi-input consolidation fixture embedded in a synthetic block.
// All prevouts are pre-populated so no RPC resolution is triggered.
func TestBitcoinConvertBlock_RealConsolidationFixture(t *testing.T) {
	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	block := &bitcoin.Block{
		Height:        btcIntegrationBlock,
		Confirmations: 1,
		Hash:          "test_block_hash",
		Tx:            []bitcoin.Transaction{*fixtureMultiInputConsolidation()},
	}

	result, err := idx.convertBlockWithPrevoutResolution(context.Background(), block)

	require.NoError(t, err)
	require.Len(t, result.Transactions, 1)

	tr := result.Transactions[0]
	assert.Equal(t, addrConsolidationSender, tr.FromAddress)
	assert.Equal(t, []string{addrConsolidationSender}, tr.FromAddresses,
		"3 same-address inputs deduplicate to 1 entry")
	assert.Equal(t, addrConsolidationRecipient, tr.ToAddress)
	assert.Equal(t, "21164", tr.Amount)
}

// ─── integration tests (require network, skipped with -short) ───────────────

// TestBitcoinExtract_Integration_KnownConsolidationTx fetches the real testnet3
// consolidation transaction and asserts on known values.
func TestBitcoinExtract_Integration_KnownConsolidationTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	tx, err := client.GetTransactionWithPrevouts(ctx, txidMultiInputConsolidation)
	require.NoError(t, err)

	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	transfers := idx.extractTransfersFromTx(tx, "testhash", btcIntegrationBlock, 1_000_000, btcIntegrationBlock)

	require.Len(t, transfers, 1)
	assert.Equal(t, addrConsolidationSender, transfers[0].FromAddress)
	assert.Equal(t, []string{addrConsolidationSender}, transfers[0].FromAddresses,
		"3 same-address inputs must deduplicate to a single FromAddresses entry")
	assert.Equal(t, addrConsolidationRecipient, transfers[0].ToAddress)
	assert.Equal(t, "21164", transfers[0].Amount)
}

// TestBitcoinExtract_Integration_KnownBatchPaymentTx fetches the real testnet3
// batch payment transaction and confirms all 3 outputs are emitted, including
// the change output back to the sender (Bug #2 fix).
func TestBitcoinExtract_Integration_KnownBatchPaymentTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	tx, err := client.GetTransactionWithPrevouts(ctx, txidBatchPaymentWithChange)
	require.NoError(t, err)

	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	transfers := idx.extractTransfersFromTx(tx, "testhash", btcIntegrationBlock, 1_000_000, btcIntegrationBlock)

	require.Len(t, transfers, 3,
		"all 3 outputs emitted: 2 payments + 1 change back to sender (Bug #2 fix)")

	toAmounts := map[string]string{}
	for _, tr := range transfers {
		assert.Equal(t, addrBatchSender, tr.FromAddress)
		toAmounts[tr.ToAddress] = tr.Amount
	}
	assert.Equal(t, "20860", toAmounts[addrBatchRecipient1])
	assert.Equal(t, "20831", toAmounts[addrBatchRecipient2])
	assert.Equal(t, "5321583", toAmounts[addrBatchSender], "change back to sender must be present")
}

// TestBitcoinExtract_Integration_KnownStressMultiSenderTx fetches the real 8-input
// testnet3 transaction and confirms Bug #1 fix: both unique sender addresses appear
// in FromAddresses, and Bug #2 fix: all outputs (including those back to senders) are emitted.
func TestBitcoinExtract_Integration_KnownStressMultiSenderTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	tx, err := client.GetTransactionWithPrevouts(ctx, txidStressMultiSender)
	require.NoError(t, err)

	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})

	addrs := idx.getAllInputAddresses(tx)
	require.Len(t, addrs, 2, "8 inputs from 2 addresses must deduplicate to 2")
	assert.Equal(t, addrStressSenderA, addrs[0])
	assert.Equal(t, addrStressSenderB, addrs[1])

	transfers := idx.extractTransfersFromTx(tx, "testhash", btcIntegrationBlock, 1_000_000, btcIntegrationBlock)
	require.Len(t, transfers, 3)

	for _, tr := range transfers {
		assert.Equal(t, addrStressSenderA, tr.FromAddress,
			"FromAddress must be first input's address (backward compat)")
		assert.Equal(t, []string{addrStressSenderA, addrStressSenderB}, tr.FromAddresses,
			"Bug #1 fix: both senders must appear in FromAddresses")
	}

	toAmounts := map[string]string{}
	for _, tr := range transfers {
		toAmounts[tr.ToAddress] = tr.Amount
	}
	assert.Equal(t, "9900000", toAmounts[addrStressExternal])
	assert.Equal(t, "7318111", toAmounts[addrStressSenderB], "Bug #2 fix: change to senderB emitted")
	assert.Equal(t, "99995016", toAmounts[addrStressSenderA], "Bug #2 fix: change to senderA emitted")
}

func TestBitcoinExtract_Integration_CoinbaseNotInTransfers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	block, err := client.GetBlockByHeight(ctx, btcIntegrationBlock, 3)
	require.NoError(t, err)
	require.NotEmpty(t, block.Tx)

	coinbase := &block.Tx[0]
	require.True(t, coinbase.IsCoinbase())

	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3"})
	assert.Empty(t, idx.extractTransfersFromTx(coinbase, block.Hash, block.Height, block.Time, block.Height))
}

func TestBitcoinExtract_Integration_UTXOEventStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newBTCTestClient(t)
	tx, err := client.GetTransactionWithPrevouts(ctx, txidBatchPaymentWithChange)
	require.NoError(t, err)

	idx := newBTCTestIndexer(config.ChainConfig{NetworkId: "testnet3", IndexUTXO: true})
	event := idx.extractUTXOEvent(tx, btcIntegrationBlock, "integration_block_hash", 1_000_000, btcIntegrationBlock)
	require.NotNil(t, event)

	assert.Equal(t, txidBatchPaymentWithChange, event.TxHash)

	// 3 spendable outputs
	require.Len(t, event.Created, 3)

	// 1 input with prevout
	require.Len(t, event.Spent, 1)
	assert.Equal(t, addrBatchSender, event.Spent[0].Address)

	fee, err := decimal.NewFromString(event.TxFee)
	require.NoError(t, err)
	assert.False(t, fee.IsNegative())

	for _, spent := range event.Spent {
		assert.Equal(t, fmt.Sprintf("%s:%d", spent.TxHash, spent.Vout), spent.Key())
	}
}
