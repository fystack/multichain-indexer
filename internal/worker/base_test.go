package worker

import (
	"context"
	"sync"
	"testing"

	"log/slog"

	"github.com/Woft257/multichain-indexer/internal/indexer"
	"github.com/Woft257/multichain-indexer/internal/rpc/cardano"
	"github.com/Woft257/multichain-indexer/pkg/common/enum"
	"github.com/Woft257/multichain-indexer/pkg/common/types"
	"github.com/Woft257/multichain-indexer/pkg/common/utils"
	"github.com/Woft257/multichain-indexer/pkg/events"
	"github.com/stretchr/testify/assert"
)

// mockPubkeyStore is a simple mock for pubkeystore.Store
type mockPubkeyStore struct {
	mu           sync.RWMutex
	watchedAddrs map[string]bool
}

func newMockPubkeyStore() *mockPubkeyStore {
	return &mockPubkeyStore{
		watchedAddrs: make(map[string]bool),
	}
}

// Save adds an address to the mock store.
func (m *mockPubkeyStore) Save(_ enum.NetworkType, address string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watchedAddrs[address] = true
	return nil
}

func (m *mockPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.watchedAddrs[address]
	return ok
}
func (m *mockPubkeyStore) Close() error { return nil } // Add Close to satisfy the interface

// mockEmitter is a simple mock for events.Emitter
type mockEmitter struct {
	mu       sync.RWMutex
	emittedTxs []*types.Transaction
}

func (m *mockEmitter) EmitTransaction(_ string, tx *types.Transaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emittedTxs = append(m.emittedTxs, tx)
	return nil
}

// Unused methods to satisfy the interface
func (m *mockEmitter) EmitBlock(string, *types.Block) error { return nil }
func (m *mockEmitter) EmitError(string, error) error       { return nil }
func (m *mockEmitter) Emit(events.IndexerEvent) error      { return nil }
func (m *mockEmitter) Close()                               {}

func (m *mockEmitter) GetEmittedTxs() []*types.Transaction {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy
	result := make([]*types.Transaction, len(m.emittedTxs))
	copy(result, m.emittedTxs)
	return result
}



// mockIndexer to satisfy the chain interface in BaseWorker
type mockIndexer struct {
	networkType enum.NetworkType
}

func (m *mockIndexer) GetName() string                  { return "mock_chain" }
func (m *mockIndexer) GetNetworkType() enum.NetworkType { return m.networkType }
func (m *mockIndexer) GetNetworkInternalCode() string   { return "" }
func (m *mockIndexer) GetLatestBlockNumber(context.Context) (uint64, error) { return 0, nil }
func (m *mockIndexer) GetBlock(context.Context, uint64) (*types.Block, error) { return nil, nil }
func (m *mockIndexer) GetBlocks(context.Context, uint64, uint64, bool) ([]indexer.BlockResult, error) {
	return nil, nil
}
func (m *mockIndexer) GetBlocksByNumbers(context.Context, []uint64) ([]indexer.BlockResult, error) {
	return nil, nil
}
func (m *mockIndexer) IsHealthy() bool { return true }

func TestBaseWorker_EmitBlock_Cardano(t *testing.T) {
	// 1. Setup
	pubkeyStore := newMockPubkeyStore()
	emitter := &mockEmitter{}

	bw := &BaseWorker{
		ctx:         context.Background(),
		logger:      slog.Default(),
		pubkeyStore: pubkeyStore,
		emitter:     emitter,
		chain:       &mockIndexer{networkType: enum.NetworkTypeCardano},
	}

	watchedAddr := "addr1q9p7z2f3y89h6y8a2nh7w7j7d8c9q0g6h5j4k3l2m1n0p8q7r6s5t4"
	pubkeyStore.Save(enum.NetworkTypeCardano, watchedAddr)

	// 2. Create Cardano test data
	richTx := &cardano.RichTransaction{
		Hash:        "tx_hash_cardano_123",
		BlockHeight: 100,
		Fee:         "170000",
		Outputs: []cardano.RichOutput{
			{
				Address: watchedAddr,
				Assets: []cardano.RichAsset{
					{Unit: "lovelace", Quantity: "5000000"},
					{Unit: "some_other_token_policy_id", Quantity: "123"},
				},
			},
			{
				Address: "some_other_address", // This one should be ignored
				Assets: []cardano.RichAsset{
					{Unit: "lovelace", Quantity: "1000000"},
				},
			},
		},
	}

	payload, err := utils.Encode(richTx)
	assert.NoError(t, err)

	block := &types.Block{
		Number:    100,
		Timestamp: 1672531200,
		Transactions: []types.Transaction{
			{
				TxHash:  richTx.Hash,
				Payload: payload,
			},
		},
	}

	// 3. Run the function to be tested
	bw.emitBlock(block)

	// 4. Assertions
	emittedTxs := emitter.GetEmittedTxs()
	assert.Equal(t, 2, len(emittedTxs), "Should have emitted two separate asset transfers")

	// Check ADA (lovelace) transfer
	adaTx := emittedTxs[0]
	assert.Equal(t, richTx.Hash, adaTx.TxHash)
	assert.Equal(t, watchedAddr, adaTx.ToAddress)
	assert.Equal(t, "5000000", adaTx.Amount)
	assert.Equal(t, "", adaTx.AssetAddress, "AssetAddress should be empty for lovelace")

	// Check custom token transfer
	tokenTx := emittedTxs[1]
	assert.Equal(t, richTx.Hash, tokenTx.TxHash)
	assert.Equal(t, watchedAddr, tokenTx.ToAddress)
	assert.Equal(t, "123", tokenTx.Amount)
	assert.Equal(t, "some_other_token_policy_id", tokenTx.AssetAddress)
}

func TestBaseWorker_EmitBlock_Legacy(t *testing.T) {
	// 1. Setup
	pubkeyStore := newMockPubkeyStore()
	emitter := &mockEmitter{}

	bw := &BaseWorker{
		ctx:         context.Background(),
		logger:      slog.Default(),
		pubkeyStore: pubkeyStore,
		emitter:     emitter,
		chain:       &mockIndexer{networkType: enum.NetworkTypeEVM},
	}

	watchedAddr := "0x1234567890123456789012345678901234567890"
	pubkeyStore.Save(enum.NetworkTypeEVM, watchedAddr)

	// 2. Create EVM test data
	block := &types.Block{
		Number:    200,
		Timestamp: 1672532200,
		Transactions: []types.Transaction{
			{
				TxHash:      "tx_hash_evm_456",
				FromAddress: "0xFromAddress",
				ToAddress:   watchedAddr,
				Amount:      "1000000000000000000",
			},
			{
				TxHash:      "tx_hash_evm_789",
				FromAddress: "0xAnotherFrom",
				ToAddress:   "0xUnwatchedAddress", // Should be ignored
				Amount:      "5000",
			},
		},
	}

	// 3. Run
	bw.emitBlock(block)

	// 4. Assertions
	emittedTxs := emitter.GetEmittedTxs()
	assert.Equal(t, 1, len(emittedTxs), "Should have emitted one transaction")

	emittedTx := emittedTxs[0]
	assert.Equal(t, "tx_hash_evm_456", emittedTx.TxHash)
	assert.Equal(t, watchedAddr, emittedTx.ToAddress)
}
