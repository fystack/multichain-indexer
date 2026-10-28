package indexer

import (
	"encoding/binary"
	"maps"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingPubkeyStore struct {
	watched    map[string]bool
	existCalls int
	batches    [][]string
}

func (m *countingPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	m.existCalls++
	return m.watched[address]
}

func (m *countingPubkeyStore) ExistBatch(_ enum.NetworkType, addresses []string) []bool {
	m.batches = append(m.batches, append([]string(nil), addresses...))
	out := make([]bool, len(addresses))
	for i, a := range addresses {
		out[i] = m.watched[a]
	}
	return out
}

func solSystemData(tag uint32, lamports uint64, extra ...byte) string {
	b := make([]byte, 12, 12+len(extra))
	binary.LittleEndian.PutUint32(b, tag)
	binary.LittleEndian.PutUint64(b[4:], lamports)
	return base58.Encode(append(b, extra...))
}

func solTokenData(prefix []byte, amount uint64, suffix ...byte) string {
	b := append([]byte(nil), prefix...)
	b = binary.LittleEndian.AppendUint64(b, amount)
	return base58.Encode(append(b, suffix...))
}

func solKeys(pubkeys ...string) []solana.AccountKey {
	out := make([]solana.AccountKey, len(pubkeys))
	for i, pk := range pubkeys {
		out[i] = solana.AccountKey{Pubkey: pk}
	}
	return out
}

const (
	kWalletA = iota
	kWalletB
	kNonce
	kWalletC
	kSeedFunding
	kSeedBase
	kWalletD
	kSrcTokenAcc
	kDstTokenAcc
	kMint
	kSystem
	kToken
	kToken2022
	kRouter
	kAuthority
)

// solTestBlock holds one transaction exercising every transfer shape the parser handles.
func solTestBlock() *solana.GetBlockResult {
	tx := solana.BlockTxn{
		Meta: &solana.TxnMeta{
			Fee: 5000,
			PreTokenBalances: []solana.TokenBalance{
				{AccountIndex: kSrcTokenAcc, Owner: "ownerX", Mint: "mintM"},
			},
			PostTokenBalances: []solana.TokenBalance{
				{AccountIndex: kSrcTokenAcc, Owner: "ownerX", Mint: "mintM"},
				{AccountIndex: kDstTokenAcc, Owner: "ownerY", Mint: "mintM"},
			},
			InnerInstructions: []solana.InnerInstruction{{
				Index: 4,
				Instructions: []solana.Instruction{
					{ProgramIdIndex: kToken, Accounts: []int{kSrcTokenAcc, kDstTokenAcc, kAuthority}, Data: solTokenData([]byte{3}, 500)},
					{ProgramIdIndex: kToken2022, Accounts: []int{kSrcTokenAcc, kMint, kDstTokenAcc, kAuthority}, Data: solTokenData([]byte{26, 1}, 600, 6, 6, 0, 0, 0, 0, 0, 0, 0)},
					// InitializeTransferFeeConfig shares the 26 tag but moves nothing.
					{ProgramIdIndex: kToken2022, Accounts: []int{kMint}, Data: solTokenData([]byte{26, 0}, 999, 0, 0, 0, 0, 0, 0, 0, 0, 0)},
					{ProgramIdIndex: kToken, Accounts: []int{kSrcTokenAcc, kMint, kDstTokenAcc, kAuthority}, Data: solTokenData([]byte{12}, 700, 6)},
				},
			}},
		},
	}
	tx.Transaction.Signatures = []string{"sig1"}
	tx.Transaction.Message.AccountKeys = solKeys(
		"walletA", "walletB", "nonceAcct", "walletC", "seedFunding", "seedBase", "walletD",
		"srcTokenAcc", "dstTokenAcc", "mintM",
		solanaSystemProgramID, solanaTokenProgramID, solanaToken2022ProgramID, "router", "authority",
	)
	seed := []byte{4, 0, 0, 0, 0, 0, 0, 0, 's', 'e', 'e', 'd'}
	tx.Transaction.Message.Instructions = []solana.Instruction{
		{ProgramIdIndex: kSystem, Accounts: []int{kWalletA, kWalletB}, Data: solSystemData(solanaSystemTransfer, 100)},
		{ProgramIdIndex: kSystem, Accounts: []int{kNonce, kWalletC, kRouter, kRouter, kWalletA}, Data: solSystemData(solanaSystemWithdrawNonceAccount, 200)},
		{ProgramIdIndex: kSystem, Accounts: []int{kSeedFunding, kSeedBase, kWalletD}, Data: solSystemData(solanaSystemTransferWithSeed, 300, append(seed, make([]byte, 32)...)...)},
		// CreateAccount funds a new account; not a transfer.
		{ProgramIdIndex: kSystem, Accounts: []int{kWalletA, kWalletB}, Data: solSystemData(0, 400, make([]byte, 40)...)},
		{ProgramIdIndex: kRouter, Accounts: []int{kWalletA}, Data: ""},
		// Malformed: recipient index out of range, and bad base58.
		{ProgramIdIndex: kSystem, Accounts: []int{kWalletA, 99}, Data: solSystemData(solanaSystemTransfer, 1)},
		{ProgramIdIndex: kSystem, Accounts: []int{kWalletA, kWalletB}, Data: "0OIl"},
	}

	failed := solana.BlockTxn{Meta: &solana.TxnMeta{Err: map[string]any{"InstructionError": []any{0, "Custom"}}}}
	failed.Transaction.Signatures = []string{"sigFailed"}
	failed.Transaction.Message.AccountKeys = solKeys("walletA", "walletB", solanaSystemProgramID)
	failed.Transaction.Message.Instructions = []solana.Instruction{
		{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: solSystemData(solanaSystemTransfer, 1)},
	}

	// v0 transaction whose recipient comes from an address lookup table.
	alt := solana.BlockTxn{Meta: &solana.TxnMeta{Fee: 7000, LoadedAddresses: &solana.LoadedAddresses{Writable: []string{"walletE"}}}}
	alt.Transaction.Signatures = []string{"sig2"}
	alt.Transaction.Message.AccountKeys = solKeys("walletA", solanaSystemProgramID)
	alt.Transaction.Message.Instructions = []solana.Instruction{
		{ProgramIdIndex: 1, Accounts: []int{0, 2}, Data: solSystemData(solanaSystemTransfer, 800)},
	}

	return &solana.GetBlockResult{Blockhash: "blockhash1", Transactions: []solana.BlockTxn{tx, failed, alt}}
}

func newSolanaParseIndexer(store PubkeyStore, twoWay bool) *SolanaIndexer {
	return &SolanaIndexer{
		chainName:   "solana_test",
		config:      config.ChainConfig{NetworkId: "solana-mainnet", TwoWayIndexing: twoWay},
		pubkeyStore: store,
	}
}

type solTransferSummary struct {
	Index, From, To, Asset, Amount string
	Type                           constant.TxType
}

func summarize(txs []types.Transaction) []solTransferSummary {
	out := make([]solTransferSummary, len(txs))
	for i, tx := range txs {
		out[i] = solTransferSummary{tx.TransferIndex, tx.FromAddress, tx.ToAddress, tx.AssetAddress, tx.Amount, tx.Type}
	}
	return out
}

func TestSolanaExtractTransfers(t *testing.T) {
	idx := newSolanaParseIndexer(nil, false)
	got := idx.extractSolanaTransfers("solana-mainnet", 100, 1234567890, solTestBlock())

	native, token := constant.TxTypeNativeTransfer, constant.TxTypeTokenTransfer
	assert.Equal(t, []solTransferSummary{
		{"0", "walletA", "walletB", "", "100", native},
		{"1", "nonceAcct", "walletC", "", "200", native},
		{"2", "seedFunding", "walletD", "", "300", native},
		{"4.0", "ownerX", "ownerY", "mintM", "500", token},
		{"4.1", "ownerX", "ownerY", "mintM", "594", token},
		{"4.3", "ownerX", "ownerY", "mintM", "700", token},
		{"0", "walletA", "walletE", "", "800", native},
	}, summarize(got))

	for _, tx := range got[:6] {
		assert.Equal(t, "sig1", tx.TxHash)
		assert.Equal(t, "5000", tx.TxFee.String())
		assert.Equal(t, "blockhash1", tx.BlockHash)
		assert.Equal(t, uint64(100), tx.BlockNumber)
	}
	assert.Equal(t, "sig2", got[6].TxHash)
	assert.Equal(t, "7000", got[6].TxFee.String())
}

func TestSolanaTransferIndexIgnoresWatchList(t *testing.T) {
	store := &countingPubkeyStore{watched: map[string]bool{"ownerY": true}}
	got := newSolanaParseIndexer(store, false).extractSolanaTransfers("solana-mainnet", 100, 0, solTestBlock())

	var indices []string
	for _, tx := range got {
		indices = append(indices, tx.TransferIndex)
	}
	assert.Equal(t, []string{"4.0", "4.1", "4.3"}, indices,
		"IDs must come from the instruction position, not from how many transfers matched")
}

func TestSolanaFilterUsesOneBatchCall(t *testing.T) {
	store := &countingPubkeyStore{watched: map[string]bool{"walletC": true, "ownerY": true}}
	got := newSolanaParseIndexer(store, false).extractSolanaTransfers("solana-mainnet", 100, 0, solTestBlock())

	assert.Zero(t, store.existCalls, "per-address Exist must not be used")
	require.Len(t, store.batches, 1)
	assert.ElementsMatch(t, []string{"walletB", "walletC", "walletD", "ownerY", "walletE"}, store.batches[0],
		"recipients only, each once")

	var indices []string
	for _, tx := range got {
		indices = append(indices, tx.TransferIndex)
	}
	assert.Equal(t, []string{"1", "4.0", "4.1", "4.3"}, indices)
}

func TestSolanaFilterTwoWay(t *testing.T) {
	store := &countingPubkeyStore{watched: map[string]bool{"walletA": true}}

	got := newSolanaParseIndexer(store, false).extractSolanaTransfers("solana-mainnet", 100, 0, solTestBlock())
	assert.Empty(t, got, "a watched sender alone does not match without two-way indexing")

	store.batches = nil
	got = newSolanaParseIndexer(store, true).extractSolanaTransfers("solana-mainnet", 100, 0, solTestBlock())
	require.Len(t, store.batches, 1)
	assert.Contains(t, store.batches[0], "walletA")
	assert.Equal(t, []solTransferSummary{
		{"0", "walletA", "walletB", "", "100", constant.TxTypeNativeTransfer},
		{"0", "walletA", "walletE", "", "800", constant.TxTypeNativeTransfer},
	}, summarize(got))
}

func TestSolanaParseInstructionErrors(t *testing.T) {
	keys := solKeys("a", "b", solanaSystemProgramID, solanaToken2022ProgramID, "mint")

	cases := []struct {
		name        string
		ix          solana.Instruction
		notTransfer bool
	}{
		{"program index out of range", solana.Instruction{ProgramIdIndex: 9}, false},
		{"bad base58", solana.Instruction{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: "0OIl"}, false},
		{"system data too short", solana.Instruction{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: base58.Encode([]byte{2, 0, 0, 0, 1})}, false},
		{"missing recipient account", solana.Instruction{ProgramIdIndex: 2, Accounts: []int{0}, Data: solSystemData(solanaSystemTransfer, 1)}, false},
		{"fee transfer data too short", solana.Instruction{ProgramIdIndex: 3, Accounts: []int{0, 4, 1}, Data: solTokenData([]byte{26, 1}, 5)}, false},
		{"zero lamports", solana.Instruction{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: solSystemData(solanaSystemTransfer, 0)}, true},
		{"other system instruction", solana.Instruction{ProgramIdIndex: 2, Accounts: []int{0, 1}, Data: solSystemData(8, 1)}, true},
		{"other fee sub-instruction", solana.Instruction{ProgramIdIndex: 3, Accounts: []int{0, 4, 1}, Data: solTokenData([]byte{26, 5}, 5, make([]byte, 9)...)}, true},
		{"unrelated program", solana.Instruction{ProgramIdIndex: 0, Data: "!!"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := solanaParseInstruction(tc.ix, keys)
			require.Error(t, err)
			if tc.notTransfer {
				assert.ErrorIs(t, err, errSolanaNotTransfer)
			} else {
				assert.NotErrorIs(t, err, errSolanaNotTransfer, "malformed input must be reported, not treated as a non-transfer")
			}
		})
	}
}

func solBalance(accountIndex uint64, owner, amount string) solana.TokenBalance {
	tb := solana.TokenBalance{AccountIndex: accountIndex, Owner: owner, Mint: "mintF"}
	tb.UiTokenAmount.Amount = amount
	return tb
}

// solFeeBlock builds one tx moving mintF from ownerX's account (0) to ownerY's accounts (1, 5).
func solFeeBlock(program string, pre, post []solana.TokenBalance, ixs ...solana.Instruction) *solana.GetBlockResult {
	tx := solana.BlockTxn{Meta: &solana.TxnMeta{PreTokenBalances: pre, PostTokenBalances: post}}
	tx.Transaction.Signatures = []string{"sigFee"}
	tx.Transaction.Message.AccountKeys = solKeys("srcAcc", "dstAcc", "mintF", program, "authority", "dstAcc2")
	tx.Transaction.Message.Instructions = ixs
	return &solana.GetBlockResult{Transactions: []solana.BlockTxn{tx}}
}

func solTransferChecked(dst int, amount uint64) solana.Instruction {
	return solana.Instruction{ProgramIdIndex: 3, Accounts: []int{0, 2, dst, 4}, Data: solTokenData([]byte{12}, amount, 6)}
}

func TestSolanaToken2022TransferFees(t *testing.T) {
	type bal = []solana.TokenBalance
	srcPre := solBalance(0, "ownerX", "1000")

	cases := []struct {
		name         string
		program      string
		pre, post    bal
		ixs          []solana.Instruction
		amounts      []string
		sourceAmount []string
	}{
		{
			name:    "TransferCheckedWithFee nets the stated fee",
			program: solanaToken2022ProgramID,
			pre:     bal{srcPre},
			post:    bal{solBalance(0, "ownerX", "900"), solBalance(1, "ownerY", "0")},
			ixs: []solana.Instruction{{
				ProgramIdIndex: 3, Accounts: []int{0, 2, 1, 4},
				Data: solTokenData([]byte{26, 1}, 100, append([]byte{6}, le64(7)...)...),
			}},
			amounts: []string{"93"}, sourceAmount: []string{"100"},
		},
		{
			name:    "TransferChecked on a fee mint uses the recipient's balance change",
			program: solanaToken2022ProgramID,
			pre:     bal{srcPre, solBalance(1, "ownerY", "100")},
			post:    bal{solBalance(0, "ownerX", "900"), solBalance(1, "ownerY", "195")},
			ixs:     []solana.Instruction{solTransferChecked(1, 100)},
			amounts: []string{"95"}, sourceAmount: []string{"100"},
		},
		{
			name:    "TransferChecked on a fee-free mint keeps the amount",
			program: solanaToken2022ProgramID,
			pre:     bal{srcPre},
			post:    bal{solBalance(0, "ownerX", "900"), solBalance(1, "ownerY", "100")},
			ixs:     []solana.Instruction{solTransferChecked(1, 100)},
			amounts: []string{"100"}, sourceAmount: []string{""},
		},
		{
			name:    "legacy Token program never has fees",
			program: solanaTokenProgramID,
			pre:     bal{srcPre},
			post:    bal{solBalance(0, "ownerX", "900"), solBalance(1, "ownerY", "40")},
			ixs:     []solana.Instruction{solTransferChecked(1, 100)},
			amounts: []string{"100"}, sourceAmount: []string{""},
		},
		{
			name:    "two transfers into one account keep the gross amount",
			program: solanaToken2022ProgramID,
			pre:     bal{srcPre},
			post:    bal{solBalance(0, "ownerX", "800"), solBalance(1, "ownerY", "190")},
			ixs:     []solana.Instruction{solTransferChecked(1, 100), solTransferChecked(1, 100)},
			amounts: []string{"100", "100"}, sourceAmount: []string{"", ""},
		},
		{
			name:    "transfers into different accounts are each netted",
			program: solanaToken2022ProgramID,
			pre:     bal{srcPre},
			post:    bal{solBalance(0, "ownerX", "800"), solBalance(1, "ownerY", "98"), solBalance(5, "ownerY", "97")},
			ixs:     []solana.Instruction{solTransferChecked(1, 100), solTransferChecked(5, 100)},
			amounts: []string{"98", "97"}, sourceAmount: []string{"100", "100"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := solFeeBlock(tc.program, tc.pre, tc.post, tc.ixs...)
			got := newSolanaParseIndexer(nil, false).extractSolanaTransfers("solana-mainnet", 1, 0, block)
			require.Len(t, got, len(tc.amounts))
			for i, tx := range got {
				assert.Equal(t, tc.amounts[i], tx.Amount, "amount %d", i)
				assert.Equal(t, tc.sourceAmount[i], tx.GetMetadataString(metadataKeySourceAmount), "source amount %d", i)
				assert.Equal(t, "ownerY", tx.ToAddress)
				assert.Equal(t, "mintF", tx.AssetAddress)
			}
		})
	}
}

func TestSolanaFeeTransferAmountByDirection(t *testing.T) {
	idx := newSolanaParseIndexer(nil, false)
	got := idx.extractSolanaTransfers("solana-mainnet", 100, 0, solTestBlock())
	var fee types.Transaction
	for _, tx := range got {
		if tx.TransferIndex == "4.1" {
			fee = tx
		}
	}
	require.Equal(t, "4.1", fee.TransferIndex)

	// The worker clones metadata before normalizing each direction.
	withOwnMetadata := func(tx types.Transaction) types.Transaction {
		tx.Metadata = maps.Clone(tx.Metadata)
		return tx
	}

	in := idx.NormalizeForDirection(withOwnMetadata(fee), types.DirectionIn)
	assert.Equal(t, "594", in.Amount, "the recipient is credited what arrived")
	assert.Nil(t, in.Metadata)

	out := idx.NormalizeForDirection(withOwnMetadata(fee), types.DirectionOut)
	assert.Equal(t, "600", out.Amount, "the sender is debited what left, fee included")
	assert.Nil(t, out.Metadata)
}

func le64(v uint64) []byte {
	return binary.LittleEndian.AppendUint64(nil, v)
}
