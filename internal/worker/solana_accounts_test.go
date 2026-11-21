package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSolOwner = "8QSZu4pQpzSaxZjB9nYTT6c6xC4yMKTPacga9evirFna"
	testSolUSDC  = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	testSolATA   = "5K5ivASYeQzKSNwHjraiJRwjFF37431ENKwm38tfPDny" // owner's USDC associated token account
)

type fakeSolanaSource struct {
	fingerprints  map[string]string
	slot          uint64
	signatures    map[string][]solana.SignatureInfo // per account, oldest first
	transactions  map[string]*types.Block
	failTx        map[string]bool
	tokenAccounts map[string][]string

	sigCalls   []string
	txCalls    []string
	floorsSeen []uint64
}

func (f *fakeSolanaSource) AccountStates(_ context.Context, keys []string) (uint64, []string, error) {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = f.fingerprints[k]
	}
	return f.slot, out, nil
}

func (f *fakeSolanaSource) NewSignatures(_ context.Context, address, until string, floor uint64) ([]solana.SignatureInfo, error) {
	f.sigCalls = append(f.sigCalls, address)
	f.floorsSeen = append(f.floorsSeen, floor)
	var out []solana.SignatureInfo
	after := until == ""
	for _, s := range f.signatures[address] {
		if !after {
			after = s.Signature == until
			continue
		}
		if s.Slot > floor {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSolanaSource) TransactionBlock(_ context.Context, signature string) (*types.Block, error) {
	f.txCalls = append(f.txCalls, signature)
	if f.failTx[signature] {
		return nil, errors.New("node unavailable")
	}
	if b, ok := f.transactions[signature]; ok {
		return b, nil
	}
	return &types.Block{}, nil
}

func (f *fakeSolanaSource) TokenAccounts(_ context.Context, owner string) ([]string, error) {
	return f.tokenAccounts[owner], nil
}

type fakeAddressLoader struct{ addresses []LoadedAddress }

func (l *fakeAddressLoader) LoadAddresses(_ context.Context, p AddressLoaderParams) ([]LoadedAddress, error) {
	var out []LoadedAddress
	for _, a := range l.addresses {
		if a.CreatedAt.After(p.After) {
			out = append(out, a)
		}
	}
	return out, nil
}

type memoryCursorStore map[string][]byte

func (m memoryCursorStore) GetAny(k string, v any) (bool, error) {
	b, ok := m[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

func (m memoryCursorStore) SetAny(k string, v any) error {
	b, err := json.Marshal(v)
	m[k] = b
	return err
}

type solanaAccountFixture struct {
	worker  *SolanaAccountWorker
	source  *fakeSolanaSource
	emitter *captureEmitter
	store   *stubBlockStore
	cursors memoryCursorStore
	clock   time.Time
}

func newSolanaAccountFixture(t *testing.T, blockModeLatest uint64) *solanaAccountFixture {
	t.Helper()
	f := &solanaAccountFixture{
		source: &fakeSolanaSource{
			fingerprints:  map[string]string{testSolOwner: "1000"},
			slot:          500,
			signatures:    map[string][]solana.SignatureInfo{},
			transactions:  map[string]*types.Block{},
			failTx:        map[string]bool{},
			tokenAccounts: map[string][]string{},
		},
		emitter: &captureEmitter{},
		store:   &stubBlockStore{latestBlock: blockModeLatest},
		cursors: memoryCursorStore{},
		clock:   time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	}
	chain := &stubIndexer{name: "SOLANA_DEVNET", internalCode: "SOL_DEVNET", networkType: enum.NetworkTypeSol, latest: 450}
	base := &BaseWorker{
		ctx:         context.Background(),
		cancel:      func() {},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		chain:       chain,
		blockStore:  f.store,
		emitter:     f.emitter,
		pubkeyStore: capturePubkeyStore{addresses: map[string]struct{}{testSolOwner: {}}},
		failedChan:  make(chan FailedBlockEvent, 1),
	}
	loader := &fakeAddressLoader{addresses: []LoadedAddress{{Address: testSolOwner, CreatedAt: f.clock.Add(-time.Hour)}}}
	f.worker = newSolanaAccountWorker(base, f.source, loader, f.cursors, config.SolanaConfig{
		WatchMode:         config.SolanaWatchAccounts,
		TokenMints:        []string{testSolUSDC},
		FullCheckInterval: 10 * time.Minute,
	})
	f.worker.now = func() time.Time { return f.clock }
	return f
}

func deposit(sig string, slot uint64, asset string) *types.Block {
	return &types.Block{Number: slot, Transactions: []types.Transaction{{
		TxHash: sig, BlockNumber: slot, ToAddress: testSolOwner, AssetAddress: asset, Amount: "5000000",
	}}}
}

func emittedHashes(e *captureEmitter) []string {
	out := make([]string, 0, len(e.txs))
	for _, tx := range e.txs {
		out = append(out, tx.TxHash)
	}
	return out
}

func TestSolanaAccountWorker_FirstPollTakesOnlyTransactionsAfterBlockMode(t *testing.T) {
	f := newSolanaAccountFixture(t, 400) // block mode indexed up to slot 400
	f.source.signatures[testSolOwner] = []solana.SignatureInfo{
		{Signature: "old", Slot: 390},
		{Signature: "sol-deposit", Slot: 420},
	}
	f.source.transactions["old"] = deposit("old", 390, "")
	f.source.transactions["sol-deposit"] = deposit("sol-deposit", 420, "")

	require.NoError(t, f.worker.poll())

	assert.Equal(t, []string{"sol-deposit"}, emittedHashes(f.emitter))
	assert.Equal(t, uint64(400), f.worker.floor)
	assert.Contains(t, f.source.floorsSeen, uint64(400))
	assert.Equal(t, []uint64{500}, f.store.savedLatest)

	var floor uint64
	found, _ := f.cursors.GetAny("solana_accounts/SOL_DEVNET/floor", &floor)
	assert.True(t, found)
	assert.Equal(t, uint64(400), floor, "the floor is kept for restarts")
}

func TestSolanaAccountWorker_FloorIsChainHeadWithoutBlockMode(t *testing.T) {
	f := newSolanaAccountFixture(t, 0)

	require.NoError(t, f.worker.poll())

	assert.Equal(t, uint64(450), f.worker.floor)
}

func TestSolanaAccountWorker_WatchesAssociatedTokenAccountAndEmitsOnce(t *testing.T) {
	f := newSolanaAccountFixture(t, 400)
	// The deposit touches the owner (creating its token account) and the token account itself.
	f.source.signatures[testSolOwner] = []solana.SignatureInfo{{Signature: "usdc-deposit", Slot: 430}}
	f.source.signatures[testSolATA] = []solana.SignatureInfo{{Signature: "usdc-deposit", Slot: 430}}
	f.source.transactions["usdc-deposit"] = deposit("usdc-deposit", 430, testSolUSDC)

	require.NoError(t, f.worker.poll())

	assert.Contains(t, f.worker.accounts, testSolATA)
	assert.Equal(t, []string{"usdc-deposit"}, emittedHashes(f.emitter))
	assert.Equal(t, []string{"usdc-deposit"}, f.source.txCalls, "fetched once")
}

func TestSolanaAccountWorker_UnchangedAccountsCostNoHistoryCalls(t *testing.T) {
	f := newSolanaAccountFixture(t, 400)
	require.NoError(t, f.worker.poll())
	f.source.sigCalls = nil

	f.clock = f.clock.Add(5 * time.Second)
	require.NoError(t, f.worker.poll())

	assert.Empty(t, f.source.sigCalls)
}

func TestSolanaAccountWorker_ChangedAccountIsSyncedFromItsCursor(t *testing.T) {
	f := newSolanaAccountFixture(t, 400)
	f.source.signatures[testSolOwner] = []solana.SignatureInfo{{Signature: "first", Slot: 410}}
	require.NoError(t, f.worker.poll())
	f.source.sigCalls = nil

	f.source.signatures[testSolOwner] = append(f.source.signatures[testSolOwner], solana.SignatureInfo{Signature: "second", Slot: 470})
	f.source.transactions["second"] = deposit("second", 470, "")
	f.source.fingerprints[testSolOwner] = "2000"
	f.clock = f.clock.Add(5 * time.Second)
	require.NoError(t, f.worker.poll())

	assert.Equal(t, []string{testSolOwner}, f.source.sigCalls, "only the changed account")
	assert.Equal(t, []string{"second"}, emittedHashes(f.emitter))
	assert.Equal(t, "second", f.worker.cursorCache[testSolOwner])
}

func TestSolanaAccountWorker_FailedFetchIsRetriedWithoutSkipping(t *testing.T) {
	f := newSolanaAccountFixture(t, 400)
	f.source.signatures[testSolOwner] = []solana.SignatureInfo{
		{Signature: "a", Slot: 410},
		{Signature: "b", Slot: 420},
		{Signature: "c", Slot: 430},
	}
	for _, s := range []string{"a", "b", "c"} {
		f.source.transactions[s] = deposit(s, 400, "")
	}
	f.source.failTx["b"] = true

	err := f.worker.poll()

	require.Error(t, err)
	assert.Equal(t, []string{"a"}, emittedHashes(f.emitter))
	assert.Equal(t, "a", f.worker.cursorCache[testSolOwner], "stops before the failed one")
	assert.Empty(t, f.store.savedLatest, "progress isn't saved past a failure")

	delete(f.source.failTx, "b")
	f.clock = f.clock.Add(5 * time.Second)
	require.NoError(t, f.worker.poll())

	assert.Equal(t, []string{"a", "b", "c"}, emittedHashes(f.emitter))
}

func TestSolanaAccountWorker_SkipsFailedTransactions(t *testing.T) {
	f := newSolanaAccountFixture(t, 400)
	f.source.signatures[testSolOwner] = []solana.SignatureInfo{{Signature: "reverted", Slot: 410, Err: map[string]any{"InstructionError": "x"}}}

	require.NoError(t, f.worker.poll())

	assert.Empty(t, f.source.txCalls)
	assert.Equal(t, "reverted", f.worker.cursorCache[testSolOwner])
}

func TestSolanaAccountWorker_FullCheckSyncsEveryAccountAndFindsTokenAccounts(t *testing.T) {
	f := newSolanaAccountFixture(t, 400)
	require.NoError(t, f.worker.poll())
	f.source.sigCalls = nil

	const otherTokenAccount = "6axzNyb7KSRzixzEU2eZx9bjENLFcoWrCdNRLJNrnNCf"
	f.source.tokenAccounts[testSolOwner] = []string{testSolATA, otherTokenAccount}
	f.clock = f.clock.Add(10 * time.Minute)
	require.NoError(t, f.worker.poll())

	assert.Contains(t, f.worker.accounts, otherTokenAccount)
	assert.ElementsMatch(t, []string{testSolOwner, testSolATA, otherTokenAccount}, f.source.sigCalls)
}

func TestSolanaAccountWorker_ParsesToken2022Mints(t *testing.T) {
	w := newSolanaAccountWorker(&BaseWorker{}, &fakeSolanaSource{}, &fakeAddressLoader{}, memoryCursorStore{},
		config.SolanaConfig{TokenMints: []string{testSolUSDC, testSolUSDC + ":token-2022"}})

	require.Len(t, w.mints, 2)
	assert.Equal(t, indexer.SolanaTokenProgramIDs[0], w.mints[0].program)
	assert.Equal(t, indexer.SolanaTokenProgramIDs[1], w.mints[1].program)
	assert.Equal(t, testSolUSDC, w.mints[1].mint)
}

func TestRecentSignatures_ForgetsOldest(t *testing.T) {
	r := newRecentSignatures(2)
	r.add("a")
	r.add("b")
	r.add("c")

	assert.False(t, r.has("a"))
	assert.True(t, r.has("b"))
	assert.True(t, r.has("c"))
}
