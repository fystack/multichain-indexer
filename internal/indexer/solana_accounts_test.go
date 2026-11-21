package indexer

import (
	"context"
	"fmt"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSolanaAPI serves a newest-first signature history and canned transactions.
type fakeSolanaAPI struct {
	history  []solana.SignatureInfo // newest first
	tx       *solana.GetTransactionResult
	accounts *solana.GetMultipleAccountsResult
	pages    int
}

func (f *fakeSolanaAPI) CallRPC(context.Context, string, any) (*rpc.RPCResponse, error) {
	return nil, nil
}
func (f *fakeSolanaAPI) Do(context.Context, string, string, any, map[string]string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSolanaAPI) GetNetworkType() string { return "sol" }
func (f *fakeSolanaAPI) GetClientType() string  { return "rpc" }
func (f *fakeSolanaAPI) GetURL() string         { return "http://fake" }
func (f *fakeSolanaAPI) Close() error           { return nil }
func (f *fakeSolanaAPI) GetSlot(context.Context) (uint64, error) {
	return 0, nil
}
func (f *fakeSolanaAPI) GetBlock(context.Context, uint64) (*solana.GetBlockResult, error) {
	return nil, nil
}
func (f *fakeSolanaAPI) GetTransaction(context.Context, string) (*solana.GetTransactionResult, error) {
	return f.tx, nil
}
func (f *fakeSolanaAPI) GetMultipleAccounts(_ context.Context, keys []string) (*solana.GetMultipleAccountsResult, error) {
	return f.accounts, nil
}
func (f *fakeSolanaAPI) GetTokenAccountsByOwner(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (f *fakeSolanaAPI) GetSignaturesForAddress(_ context.Context, _ string, o solana.SignaturesOptions) ([]solana.SignatureInfo, error) {
	f.pages++
	start := 0
	if o.Before != "" {
		for i, s := range f.history {
			if s.Signature == o.Before {
				start = i + 1
			}
		}
	}
	var out []solana.SignatureInfo
	for _, s := range f.history[start:] {
		if s.Signature == o.Until || len(out) == o.Limit {
			break
		}
		out = append(out, s)
	}
	return out, nil
}

func newFakeSolanaIndexer(t *testing.T, api *fakeSolanaAPI) *SolanaIndexer {
	t.Helper()
	f := rpc.NewFailover[solana.SolanaAPI](nil)
	require.NoError(t, f.AddProvider(&rpc.Provider{Name: "fake", URL: "http://fake", Client: api, State: rpc.StateHealthy}))
	return NewSolanaIndexer("solana_devnet", config.ChainConfig{NetworkId: "solana_devnet"}, f, nil)
}

func history(n int, topSlot uint64) []solana.SignatureInfo {
	out := make([]solana.SignatureInfo, n)
	for i := range out {
		out[i] = solana.SignatureInfo{Signature: fmt.Sprintf("sig-%d", n-i), Slot: topSlot - uint64(i)}
	}
	return out
}

func TestNewSignatures_PagesBackToFloorOldestFirst(t *testing.T) {
	api := &fakeSolanaAPI{history: history(2500, 10_000)} // slots 10000 down to 7501
	idx := newFakeSolanaIndexer(t, api)

	sigs, err := idx.NewSignatures(context.Background(), "addr", "", 8000)

	require.NoError(t, err)
	require.Len(t, sigs, 2000) // slots 8001..10000
	assert.Equal(t, uint64(8001), sigs[0].Slot)
	assert.Equal(t, uint64(10_000), sigs[len(sigs)-1].Slot)
	assert.Equal(t, 3, api.pages)
}

func TestNewSignatures_StopsAtCursor(t *testing.T) {
	api := &fakeSolanaAPI{history: history(10, 100)} // sig-10 (slot 100) .. sig-1 (slot 91)
	idx := newFakeSolanaIndexer(t, api)

	sigs, err := idx.NewSignatures(context.Background(), "addr", "sig-7", 0)

	require.NoError(t, err)
	got := make([]string, len(sigs))
	for i, s := range sigs {
		got[i] = s.Signature
	}
	assert.Equal(t, []string{"sig-8", "sig-9", "sig-10"}, got)
	assert.Equal(t, 1, api.pages)
}

func TestTransactionBlock_UsesPositionInBlock(t *testing.T) {
	idxInBlock := 13
	tx := &solana.GetTransactionResult{Slot: 524733300, TransactionIndex: &idxInBlock, Meta: &solana.TxnMeta{Fee: 5000}}
	tx.Transaction.Signatures = []string{"sig"}
	tx.Transaction.Message.AccountKeys = []solana.AccountKey{{Pubkey: "from"}, {Pubkey: "to"}}
	tx.Transaction.Message.Instructions = []solana.Instruction{{
		ProgramId: solanaSystemProgramID,
		Parsed: map[string]any{"type": "transfer", "info": map[string]any{
			"source": "from", "destination": "to", "lamports": float64(1_000_000),
		}},
	}}
	idx := newFakeSolanaIndexer(t, &fakeSolanaAPI{tx: tx})

	block, err := idx.TransactionBlock(context.Background(), "sig")

	require.NoError(t, err)
	assert.Equal(t, uint64(524733300), block.Number)
	require.Len(t, block.Transactions, 1)
	assert.Equal(t, "13:0", block.Transactions[0].TransferIndex)
	assert.Equal(t, "1000000", block.Transactions[0].Amount)
	assert.Equal(t, "to", block.Transactions[0].ToAddress)
}

func TestAccountStates_FingerprintsChangeWithBalance(t *testing.T) {
	res := &solana.GetMultipleAccountsResult{Value: []*solana.AccountInfo{
		nil,
		{Lamports: 5, Owner: "11111111111111111111111111111111", Data: []string{"", "base64"}},
	}}
	res.Context.Slot = 77
	idx := newFakeSolanaIndexer(t, &fakeSolanaAPI{accounts: res})

	slot, fps, err := idx.AccountStates(context.Background(), []string{"missing", "wallet"})

	require.NoError(t, err)
	assert.Equal(t, uint64(77), slot)
	assert.Equal(t, "", fps[0])
	assert.NotEmpty(t, fps[1])
	res.Value[1].Lamports = 6
	_, fps2, _ := idx.AccountStates(context.Background(), []string{"missing", "wallet"})
	assert.NotEqual(t, fps[1], fps2[1])
}
