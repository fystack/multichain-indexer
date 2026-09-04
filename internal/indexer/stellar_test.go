package indexer

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/stellar"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStellarPubkeyStore struct {
	addresses map[string]struct{}
}

func (m mockStellarPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	_, ok := m.addresses[address]
	return ok
}

type mockStellarAPI struct {
	mu           sync.Mutex
	latest       uint64
	ledgers      map[uint64]*stellar.Ledger
	pages        map[uint64]map[string]*stellar.PaymentsPage
	operations   map[uint64]map[string]*stellar.OperationsPage
	effects      map[string]map[string]*stellar.EffectsPage
	transactions map[string]*stellar.Transaction
	txCalls      map[string]int
	effectCalls  map[string]int
}

var _ stellar.StellarAPI = (*mockStellarAPI)(nil)

func (m *mockStellarAPI) CallRPC(context.Context, string, any) (*rpc.RPCResponse, error) {
	return nil, nil
}
func (m *mockStellarAPI) Do(context.Context, string, string, any, map[string]string) ([]byte, error) {
	return nil, nil
}
func (m *mockStellarAPI) GetNetworkType() string { return rpc.NetworkStellar }
func (m *mockStellarAPI) GetClientType() string  { return rpc.ClientTypeREST }
func (m *mockStellarAPI) GetURL() string         { return "https://stellar.test" }
func (m *mockStellarAPI) Close() error           { return nil }

func (m *mockStellarAPI) GetLatestLedgerSequence(context.Context) (uint64, error) {
	return m.latest, nil
}
func (m *mockStellarAPI) GetLedger(_ context.Context, sequence uint64) (*stellar.Ledger, error) {
	return m.ledgers[sequence], nil
}
func (m *mockStellarAPI) GetPaymentsByLedger(_ context.Context, sequence uint64, cursor string, limit int) (*stellar.PaymentsPage, error) {
	if pageSet, ok := m.pages[sequence]; ok {
		return pageSet[cursor], nil
	}
	return nil, nil
}
func (m *mockStellarAPI) GetOperationsByLedger(_ context.Context, sequence uint64, cursor string, limit int) (*stellar.OperationsPage, error) {
	if pageSet, ok := m.operations[sequence]; ok {
		return pageSet[cursor], nil
	}
	return nil, nil
}
func (m *mockStellarAPI) GetEffectsByOperation(_ context.Context, operationID string, cursor string, limit int) (*stellar.EffectsPage, error) {
	m.mu.Lock()
	if m.effectCalls != nil {
		m.effectCalls[operationID]++
	}
	m.mu.Unlock()
	if pageSet, ok := m.effects[operationID]; ok {
		return pageSet[cursor], nil
	}
	return nil, nil
}
func (m *mockStellarAPI) GetTransaction(_ context.Context, hash string) (*stellar.Transaction, error) {
	m.mu.Lock()
	if m.txCalls != nil {
		m.txCalls[hash]++
	}
	m.mu.Unlock()
	return m.transactions[hash], nil
}

const stellarMainnetHorizon = "https://horizon.stellar.org"

func newTestStellarLiveIndexer(t *testing.T, kvstore infra.KVStore) (*StellarIndexer, *stellar.Client) {
	t.Helper()

	client := stellar.NewClient(stellarMainnetHorizon, nil, 30*time.Second, nil)
	t.Cleanup(func() {
		_ = client.Close()
	})

	failover := rpc.NewFailover[stellar.StellarAPI](nil)
	require.NoError(t, failover.AddProvider(&rpc.Provider{
		Name:       "stellar-mainnet",
		URL:        stellarMainnetHorizon,
		Network:    "stellar_mainnet",
		ClientType: rpc.ClientTypeREST,
		Client:     client,
		State:      rpc.StateHealthy,
	}))

	return NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet", TwoWayIndexing: true},
		failover,
		kvstore,
		nil,
	), client
}

func findStellarPayment(t *testing.T, payments []stellar.Payment, pagingToken string) stellar.Payment {
	t.Helper()

	for _, payment := range payments {
		if payment.PagingToken == pagingToken {
			return payment
		}
	}

	t.Fatalf("payment with paging token %s not found", pagingToken)
	return stellar.Payment{}
}

func findStellarOperation(t *testing.T, operations []stellar.Operation, operationID string) stellar.Operation {
	t.Helper()

	for _, operation := range operations {
		if operation.ID == operationID {
			return operation
		}
	}

	t.Fatalf("operation %s not found", operationID)
	return stellar.Operation{}
}

type mockKVStore struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func newMockKVStore() *mockKVStore {
	return &mockKVStore{items: make(map[string][]byte)}
}

func (m *mockKVStore) GetName() string { return "mock" }

func (m *mockKVStore) Set(k string, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[k] = []byte(v)
	return nil
}

func (m *mockKVStore) Get(k string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.items[k]
	if !ok {
		return "", nil
	}
	return string(val), nil
}

func (m *mockKVStore) SetAny(k string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[k] = data
	return nil
}

func (m *mockKVStore) GetAny(k string, v any) (bool, error) {
	m.mu.RLock()
	data, ok := m.items[k]
	m.mu.RUnlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(data, v)
}

func (m *mockKVStore) List(prefix string) ([]*infra.KVPair, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*infra.KVPair, 0)
	for key, value := range m.items {
		if len(prefix) == 0 || key == prefix || (len(key) > len(prefix) && key[:len(prefix)] == prefix) {
			result = append(result, &infra.KVPair{Key: key, Value: value})
		}
	}
	return result, nil
}

func (m *mockKVStore) Delete(k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, k)
	return nil
}

func (m *mockKVStore) BatchSet(pairs []infra.KVPair) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pair := range pairs {
		m.items[pair.Key] = pair.Value
	}
	return nil
}

func (m *mockKVStore) Close() error { return nil }

func TestStellarGetBlock_ParsesNativeAndIssuedPaymentsWithMemo(t *testing.T) {
	t.Parallel()

	api := &mockStellarAPI{
		latest: 15,
		ledgers: map[uint64]*stellar.Ledger{
			10: {
				Hash:     "LEDGER_HASH",
				PrevHash: "PARENT_HASH",
				Sequence: 10,
				ClosedAt: "2026-03-18T10:00:00Z",
			},
		},
		pages: map[uint64]map[string]*stellar.PaymentsPage{
			10: {
				"": {
					Embedded: struct {
						Records []stellar.Payment `json:"records"`
					}{
						Records: []stellar.Payment{
							{
								ID:                    "1",
								PagingToken:           "pt1",
								Type:                  "payment",
								TransactionHash:       "tx-native",
								TransactionSuccessful: true,
								From:                  "GFROM",
								To:                    "GDEST",
								Amount:                "12.5000000",
								AssetType:             "native",
								CreatedAt:             "2026-03-18T10:00:00Z",
							},
							{
								ID:                    "2",
								PagingToken:           "pt2",
								Type:                  "payment",
								TransactionHash:       "tx-token",
								TransactionSuccessful: true,
								From:                  "GISSUERFROM",
								To:                    "GDEST",
								Amount:                "99.2500000",
								AssetType:             "credit_alphanum4",
								AssetCode:             "USDC",
								AssetIssuer:           "GISSUER",
								CreatedAt:             "2026-03-18T10:00:02Z",
							},
							{
								ID:                    "3",
								PagingToken:           "pt3",
								Type:                  "create_account",
								TransactionHash:       "tx-create",
								TransactionSuccessful: true,
								Funder:                "GCREATOR",
								Account:               "GDEST",
								StartingBalance:       "10000.0000000",
								CreatedAt:             "2026-03-18T10:00:03Z",
							},
							{
								ID:                    "4",
								PagingToken:           "pt4",
								Type:                  "path_payment_strict_send",
								TransactionHash:       "tx-path",
								TransactionSuccessful: true,
								From:                  "GPATHFROM",
								To:                    "GDEST",
								Amount:                "7.1250000",
								AssetType:             "native",
								CreatedAt:             "2026-03-18T10:00:04Z",
							},
						},
					},
				},
			},
		},
		transactions: map[string]*stellar.Transaction{
			"tx-native": {
				Hash:       "tx-native",
				Successful: true,
				FeeCharged: "100",
				Memo:       "memo-1",
				MemoType:   "text",
				Ledger:     10,
				CreatedAt:  "2026-03-18T10:00:00Z",
			},
			"tx-token": {
				Hash:       "tx-token",
				Successful: true,
				FeeCharged: "200",
				Memo:       "memo-2",
				MemoType:   "text",
				Ledger:     10,
				CreatedAt:  "2026-03-18T10:00:02Z",
			},
			"tx-create": {
				Hash:       "tx-create",
				Successful: true,
				FeeCharged: "100",
				Memo:       "",
				MemoType:   "none",
				Ledger:     10,
				CreatedAt:  "2026-03-18T10:00:03Z",
			},
			"tx-path": {
				Hash:       "tx-path",
				Successful: true,
				FeeCharged: "300",
				Memo:       "memo-3",
				MemoType:   "text",
				Ledger:     10,
				CreatedAt:  "2026-03-18T10:00:04Z",
			},
		},
	}

	failover := rpc.NewFailover[stellar.StellarAPI](nil)
	require.NoError(t, failover.AddProvider(&rpc.Provider{
		Name:       "stellar-test",
		URL:        "https://stellar.test",
		Network:    "stellar_mainnet",
		ClientType: rpc.ClientTypeREST,
		Client:     api,
		State:      rpc.StateHealthy,
	}))

	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		failover,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GDEST": {}}},
	)

	block, err := idx.GetBlock(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 4)

	nativeTx := block.Transactions[0]
	assert.Equal(t, constant.TxTypeNativeTransfer, nativeTx.Type)
	assert.Equal(t, "", nativeTx.AssetAddress)
	assert.Equal(t, "LEDGER_HASH", nativeTx.BlockHash)
	assert.Equal(t, "pt1", nativeTx.TransferIndex)
	memo := nativeTx.Memo
	assert.Equal(t, "memo-1", memo)
	memoType := nativeTx.MemoType
	assert.Equal(t, types.MemoTypeText, memoType)
	assert.Equal(t, "0.00001", nativeTx.TxFee.String())

	tokenTx := block.Transactions[1]
	assert.Equal(t, constant.TxTypeTokenTransfer, tokenTx.Type)
	assert.Equal(t, "GISSUER:USDC", tokenTx.AssetAddress)
	assert.Equal(t, "LEDGER_HASH", tokenTx.BlockHash)
	assert.Equal(t, "pt2", tokenTx.TransferIndex)
	memo = tokenTx.Memo
	assert.Equal(t, "memo-2", memo)
	assert.Equal(t, "99.2500000", tokenTx.Amount)

	createTx := block.Transactions[2]
	assert.Equal(t, constant.TxTypeNativeTransfer, createTx.Type)
	assert.Equal(t, "LEDGER_HASH", createTx.BlockHash)
	assert.Equal(t, "pt3", createTx.TransferIndex)
	assert.Equal(t, "GCREATOR", createTx.FromAddress)
	assert.Equal(t, "GDEST", createTx.ToAddress)
	assert.Equal(t, "10000.0000000", createTx.Amount)

	pathTx := block.Transactions[3]
	assert.Equal(t, constant.TxTypeNativeTransfer, pathTx.Type)
	assert.Equal(t, "LEDGER_HASH", pathTx.BlockHash)
	assert.Equal(t, "pt4", pathTx.TransferIndex)
	assert.Equal(t, "GPATHFROM", pathTx.FromAddress)
	assert.Equal(t, "GDEST", pathTx.ToAddress)
	assert.Equal(t, "7.1250000", pathTx.Amount)
	memo = pathTx.Memo
	assert.Equal(t, "memo-3", memo)
	assert.Equal(t, "0.00003", pathTx.TxFee.String())
}

func TestStellarConvertPayment_PathPaymentStoresSourceSideRouting(t *testing.T) {
	t.Parallel()

	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		nil,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GDEST": {}}},
	)

	tx, ok := idx.convertPayment(
		stellar.Payment{
			Type:                  "path_payment_strict_send",
			TransactionHash:       "tx-path",
			TransactionSuccessful: true,
			From:                  "GSOURCE",
			To:                    "GDEST",
			Amount:                "5.0000000",
			AssetType:             "credit_alphanum4",
			AssetCode:             "USDC",
			AssetIssuer:           "GISSUER",
			SourceAmount:          "10.0000000",
			SourceAssetType:       "native",
		},
		nil,
		21,
		"ledger-hash",
		"payment-1",
		123,
	)
	require.True(t, ok)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "GISSUER:USDC", tx.AssetAddress)
	assert.Equal(t, "5.0000000", tx.Amount)
	assert.Equal(t, string(constant.TxTypeNativeTransfer), tx.GetMetadataString(metadataKeySourceTxType))
	assert.Equal(t, "10.0000000", tx.GetMetadataString(metadataKeySourceAmount))
	assert.Equal(t, "", tx.GetMetadataString(metadataKeySourceAsset))

	outTx := idx.NormalizeForDirection(tx, types.DirectionOut)
	assert.Equal(t, constant.TxTypeNativeTransfer, outTx.Type)
	assert.Equal(t, "", outTx.AssetAddress)
	assert.Equal(t, "10.0000000", outTx.Amount)
	assert.Equal(t, "", outTx.GetMetadataString(metadataKeySourceTxType))
	assert.Equal(t, "", outTx.GetMetadataString(metadataKeySourceAmount))
	assert.Equal(t, "", outTx.GetMetadataString(metadataKeySourceAsset))
}

func TestStellarGetBlock_FetchesTransactionDetailsLazily(t *testing.T) {
	t.Parallel()

	api := &mockStellarAPI{
		latest: 20,
		ledgers: map[uint64]*stellar.Ledger{
			20: {
				Hash:     "LEDGER_HASH",
				PrevHash: "PARENT_HASH",
				Sequence: 20,
				ClosedAt: "2026-03-18T11:00:00Z",
			},
		},
		pages: map[uint64]map[string]*stellar.PaymentsPage{
			20: {
				"": {
					Embedded: struct {
						Records []stellar.Payment `json:"records"`
					}{
						Records: []stellar.Payment{
							{
								ID:                    "1",
								PagingToken:           "pt1",
								Type:                  "payment",
								TransactionHash:       "tx-pay",
								TransactionSuccessful: true,
								From:                  "GFROM",
								To:                    "GDEST",
								Amount:                "1.0000000",
								AssetType:             "native",
							},
							{
								ID:                    "2",
								PagingToken:           "pt2",
								Type:                  "payment",
								TransactionHash:       "tx-pay",
								TransactionSuccessful: true,
								From:                  "GFROM2",
								To:                    "GDEST",
								Amount:                "2.0000000",
								AssetType:             "native",
							},
						},
					},
				},
			},
		},
		operations: map[uint64]map[string]*stellar.OperationsPage{
			20: {
				"": {
					Embedded: struct {
						Records []stellar.Operation `json:"records"`
					}{
						Records: []stellar.Operation{
							{
								ID:                    "op-ignored",
								PagingToken:           "op-ignored",
								Type:                  "manage_sell_offer",
								TransactionHash:       "tx-ignored",
								TransactionSuccessful: true,
							},
							{
								ID:                    "op-create",
								PagingToken:           "op-create",
								Type:                  "create_claimable_balance",
								TransactionHash:       "tx-create",
								TransactionSuccessful: true,
								SourceAccount:         "GSOURCE",
								Asset:                 "USD:GISSUER",
								Amount:                "5.5000000",
								Claimants: []stellar.Claimant{
									{Destination: "GCLAIMANT"},
								},
							},
							{
								ID:                    "op-claim",
								PagingToken:           "op-claim",
								Type:                  "claim_claimable_balance",
								TransactionHash:       "tx-claim",
								TransactionSuccessful: true,
								BalanceID:             "balance-2",
								Claimant:              "GCLAIMANT",
							},
							{
								ID:                    "op-clawback",
								PagingToken:           "op-clawback",
								Type:                  "clawback_claimable_balance",
								TransactionHash:       "tx-clawback",
								TransactionSuccessful: true,
								BalanceID:             "balance-3",
							},
						},
					},
				},
			},
		},
		effects: map[string]map[string]*stellar.EffectsPage{
			"op-create": {
				"": {
					Embedded: struct {
						Records []stellar.Effect `json:"records"`
					}{
						Records: []stellar.Effect{
							{
								Type:      "claimable_balance_created",
								BalanceID: "balance-1",
								Asset:     "USD:GISSUER",
								Amount:    "5.5000000",
							},
						},
					},
				},
			},
			"op-claim": {
				"": {
					Embedded: struct {
						Records []stellar.Effect `json:"records"`
					}{
						Records: []stellar.Effect{
							{
								Type:      "claimable_balance_claimed",
								Account:   "GCLAIMANT",
								BalanceID: "balance-2",
								Asset:     "USD:GISSUER",
								Amount:    "4.2500000",
							},
						},
					},
				},
			},
			"op-clawback": {
				"": {
					Embedded: struct {
						Records []stellar.Effect `json:"records"`
					}{
						Records: []stellar.Effect{
							{
								Type:      "claimable_balance_clawed_back",
								Account:   "GCLAIMANT",
								BalanceID: "balance-3",
								Asset:     "USD:GISSUER",
								Amount:    "1.7500000",
							},
						},
					},
				},
			},
		},
		transactions: map[string]*stellar.Transaction{
			"tx-pay": {
				Hash:       "tx-pay",
				Successful: true,
				FeeCharged: "100",
				Ledger:     20,
				CreatedAt:  "2026-03-18T11:00:01Z",
			},
			"tx-create": {
				Hash:       "tx-create",
				Successful: true,
				FeeCharged: "200",
				Ledger:     20,
				CreatedAt:  "2026-03-18T11:00:02Z",
			},
			"tx-claim": {
				Hash:       "tx-claim",
				Successful: true,
				FeeCharged: "210",
				Ledger:     20,
				CreatedAt:  "2026-03-18T11:00:02Z",
			},
			"tx-clawback": {
				Hash:       "tx-clawback",
				Successful: true,
				FeeCharged: "220",
				Ledger:     20,
				CreatedAt:  "2026-03-18T11:00:02Z",
			},
			"tx-ignored": {
				Hash:       "tx-ignored",
				Successful: true,
				FeeCharged: "300",
				Ledger:     20,
				CreatedAt:  "2026-03-18T11:00:03Z",
			},
		},
		txCalls:     map[string]int{},
		effectCalls: map[string]int{},
	}

	failover := rpc.NewFailover[stellar.StellarAPI](nil)
	require.NoError(t, failover.AddProvider(&rpc.Provider{
		Name:       "stellar-test",
		URL:        "https://stellar.test",
		Network:    "stellar_mainnet",
		ClientType: rpc.ClientTypeREST,
		Client:     api,
		State:      rpc.StateHealthy,
	}))

	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		failover,
		newMockKVStore(),
		mockStellarPubkeyStore{addresses: map[string]struct{}{
			"GDEST":     {},
			"GCLAIMANT": {},
		}},
	)

	block, err := idx.GetBlock(context.Background(), 20)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 3)

	assert.Equal(t, 1, api.txCalls["tx-pay"])
	assert.Equal(t, 1, api.txCalls["tx-create"])
	assert.Equal(t, 1, api.txCalls["tx-claim"])
	assert.Equal(t, 1, api.txCalls["tx-clawback"])
	assert.Zero(t, api.txCalls["tx-ignored"])
	assert.Equal(t, 1, api.effectCalls["op-create"])
	assert.Equal(t, 1, api.effectCalls["op-claim"])
	assert.Zero(t, api.effectCalls["op-clawback"])

	claimTx := block.Transactions[2]
	assert.Empty(t, claimTx.FromAddress)
	assert.Equal(t, "GCLAIMANT", claimTx.ToAddress)
	assert.Equal(t, "GISSUER:USD", claimTx.AssetAddress)
	assert.Equal(t, "4.2500000", claimTx.Amount)
	assert.Equal(t, "claim_claimable_balance", claimTx.GetMetadataString(types.MetadataKeySubtype))
}

func TestFetchStellarLedgerPayments_PaginatesWithoutDuplication(t *testing.T) {
	t.Parallel()

	api := &mockStellarAPI{
		pages: map[uint64]map[string]*stellar.PaymentsPage{
			11: {
				"": {
					Embedded: struct {
						Records []stellar.Payment `json:"records"`
					}{
						Records: []stellar.Payment{
							{PagingToken: "p1", TransactionHash: "tx1"},
							{PagingToken: "p2", TransactionHash: "tx2"},
						},
					},
				},
				"p2": {
					Embedded: struct {
						Records []stellar.Payment `json:"records"`
					}{
						Records: []stellar.Payment{
							{PagingToken: "p3", TransactionHash: "tx3"},
						},
					},
				},
			},
		},
	}

	payments, err := fetchStellarLedgerPayments(context.Background(), api, 11)
	require.NoError(t, err)
	require.Len(t, payments, 3)
	assert.Equal(t, "tx1", payments[0].TransactionHash)
	assert.Equal(t, "tx2", payments[1].TransactionHash)
	assert.Equal(t, "tx3", payments[2].TransactionHash)
}

func TestStellarConvertPayment_RespectsTwoWayIndexing(t *testing.T) {
	t.Parallel()

	payment := stellar.Payment{
		Type:                  "payment",
		TransactionHash:       "tx-out",
		TransactionSuccessful: true,
		From:                  "GMONITORED",
		To:                    "GOTHER",
		Amount:                "1.0000000",
		AssetType:             "native",
	}

	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		nil,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GMONITORED": {}}},
	)
	_, ok := idx.convertPayment(payment, nil, 12, "ledger-hash", "payment-0", 1)
	require.False(t, ok)

	idx = NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet", TwoWayIndexing: true},
		nil,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GMONITORED": {}}},
	)
	_, ok = idx.convertPayment(payment, nil, 12, "ledger-hash", "payment-0", 1)
	require.True(t, ok)
}

func TestStellarConvertPayment_CreateAccountUsesFunderAndStartingBalance(t *testing.T) {
	t.Parallel()

	payment := stellar.Payment{
		Type:                  "create_account",
		TransactionHash:       "tx-create",
		TransactionSuccessful: true,
		Funder:                "GFUNDER",
		Account:               "GNEW",
		StartingBalance:       "10000.0000000",
	}

	idx := NewStellarIndexer(
		"stellar_testnet",
		config.ChainConfig{NetworkId: "stellar_testnet"},
		nil,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GNEW": {}}},
	)

	tx, ok := idx.convertPayment(payment, nil, 42, "ledger-hash", "payment-1", 123)
	require.True(t, ok)
	assert.Equal(t, "ledger-hash", tx.BlockHash)
	assert.Equal(t, "payment-1", tx.TransferIndex)
	assert.Equal(t, "GFUNDER", tx.FromAddress)
	assert.Equal(t, "GNEW", tx.ToAddress)
	assert.Equal(t, "10000.0000000", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
}

func TestStellarConvertPayment_AccountMergeUsesIntoAndAmount(t *testing.T) {
	t.Parallel()

	payment := stellar.Payment{
		Type:                  "account_merge",
		TransactionHash:       "tx-merge",
		TransactionSuccessful: true,
		Account:               "GMERGED",
		Into:                  "GDEST",
		Amount:                "8.5000000",
	}

	idx := NewStellarIndexer(
		"stellar_testnet",
		config.ChainConfig{NetworkId: "stellar_testnet"},
		nil,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GDEST": {}}},
	)

	tx, ok := idx.convertPayment(payment, nil, 52, "ledger-hash", "payment-2", 123)
	require.True(t, ok)
	assert.Equal(t, "ledger-hash", tx.BlockHash)
	assert.Equal(t, "payment-2", tx.TransferIndex)
	assert.Equal(t, "GMERGED", tx.FromAddress)
	assert.Equal(t, "GDEST", tx.ToAddress)
	assert.Equal(t, "8.5000000", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
}

func TestStellarConvertOperation_ClawbackUsesBurnAddress(t *testing.T) {
	t.Parallel()

	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		nil,
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GHOLDER": {}}},
	)

	tx, ok, err := idx.convertOperation(
		stellar.Operation{
			ID:                    "10",
			PagingToken:           "10",
			Type:                  "clawback",
			TransactionHash:       "tx-claw",
			TransactionSuccessful: true,
			From:                  "GHOLDER",
			Asset:                 "USDC:GISSUER",
			Amount:                "5.0000000",
			CreatedAt:             "2026-03-18T10:00:00Z",
		},
		nil,
		&stellar.Transaction{FeeCharged: "100"},
		77,
		"ledger-hash",
		"10",
		123,
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "GHOLDER", tx.FromAddress)
	assert.Equal(t, stellarBurnAddress, tx.ToAddress)
	assert.Equal(t, "GISSUER:USDC", tx.AssetAddress)
	assert.Equal(t, "5.0000000", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "ledger-hash", tx.BlockHash)
	assert.Equal(t, "10", tx.TransferIndex)
	subtype := tx.GetMetadataString(types.MetadataKeySubtype)
	assert.Equal(t, "clawback", subtype)
}

func TestStellarConvertOperation_CreateClaimableBalancePersistsState(t *testing.T) {
	t.Parallel()

	kv := newMockKVStore()
	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		nil,
		kv,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GCLAIM": {}}},
	)

	tx, ok, err := idx.convertOperation(
		stellar.Operation{
			ID:                    "200",
			PagingToken:           "200",
			Type:                  "create_claimable_balance",
			TransactionHash:       "tx-create-balance",
			TransactionSuccessful: true,
			SourceAccount:         "GFUNDER",
			Asset:                 "USDC:GISSUER",
			Amount:                "11.5000000",
			Claimants:             []stellar.Claimant{{Destination: "GCLAIM"}},
			CreatedAt:             "2026-03-18T10:00:00Z",
		},
		[]stellar.Effect{
			{Type: "claimable_balance_created", BalanceID: "balance-1"},
		},
		&stellar.Transaction{FeeCharged: "100"},
		77,
		"ledger-hash",
		"200",
		123,
	)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, tx.FromAddress)
	assert.Empty(t, tx.ToAddress)

	var state stellarClaimableBalanceState
	found, err := kv.GetAny(idx.claimableBalanceStateKey("balance-1"), &state)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "USDC:GISSUER", state.Asset)
	assert.Equal(t, "11.5000000", state.Amount)
	assert.Equal(t, "GFUNDER", state.SourceAccount)
	assert.Equal(t, []string{"GCLAIM"}, state.Claimants)
}

func TestStellarConvertOperation_CreateClaimableBalanceEmitsSourceDebitWhenTwoWayIndexed(t *testing.T) {
	t.Parallel()

	kv := newMockKVStore()
	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet", TwoWayIndexing: true},
		nil,
		kv,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GFUNDER": {}}},
	)

	tx, ok, err := idx.convertOperation(
		stellar.Operation{
			ID:                    "201",
			PagingToken:           "201",
			Type:                  "create_claimable_balance",
			TransactionHash:       "tx-create-balance",
			TransactionSuccessful: true,
			SourceAccount:         "GFUNDER",
			Asset:                 "USDC:GISSUER",
			Amount:                "11.5000000",
			Claimants:             []stellar.Claimant{{Destination: "GCLAIM"}},
		},
		[]stellar.Effect{
			{Type: "claimable_balance_created", BalanceID: "balance-1"},
		},
		&stellar.Transaction{FeeCharged: "100"},
		77,
		"ledger-hash",
		"201",
		123,
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "GFUNDER", tx.FromAddress)
	assert.Empty(t, tx.ToAddress)
	assert.Equal(t, "GISSUER:USDC", tx.AssetAddress)
	assert.Equal(t, "11.5000000", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "create_claimable_balance", tx.GetMetadataString(types.MetadataKeySubtype))
	assert.Equal(t, "balance-1", tx.GetMetadataString(types.MetadataKeyClaimableID))
}

func TestStellarConvertOperation_ClaimClaimableBalanceConsumesState(t *testing.T) {
	t.Parallel()

	kv := newMockKVStore()
	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		nil,
		kv,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GCLAIM": {}}},
	)
	require.NoError(t, idx.saveClaimableBalanceState("balance-2", stellarClaimableBalanceState{
		Asset:         "USDC:GISSUER",
		Amount:        "7.0000000",
		SourceAccount: "GFUNDER",
		Claimants:     []string{"GCLAIM"},
	}))

	tx, ok, err := idx.convertOperation(
		stellar.Operation{
			ID:                    "201",
			PagingToken:           "201",
			Type:                  "claim_claimable_balance",
			TransactionHash:       "tx-claim-balance",
			TransactionSuccessful: true,
			BalanceID:             "balance-2",
			Claimant:              "GCLAIM",
		},
		nil,
		&stellar.Transaction{FeeCharged: "100"},
		78,
		"ledger-hash",
		"201",
		123,
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, tx.FromAddress)
	assert.Equal(t, "GCLAIM", tx.ToAddress)
	assert.Equal(t, "GISSUER:USDC", tx.AssetAddress)
	assert.Equal(t, "7.0000000", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)

	var state stellarClaimableBalanceState
	found, err := kv.GetAny(idx.claimableBalanceStateKey("balance-2"), &state)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestStellarConvertOperation_ClawbackClaimableBalanceConsumesState(t *testing.T) {
	t.Parallel()

	kv := newMockKVStore()
	idx := NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet"},
		nil,
		kv,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GCLAIM": {}}},
	)
	require.NoError(t, idx.saveClaimableBalanceState("balance-3", stellarClaimableBalanceState{
		Asset:         "USDC:GISSUER",
		Amount:        "3.5000000",
		SourceAccount: "GFUNDER",
		Claimants:     []string{"GCLAIM"},
	}))

	tx, ok, err := idx.convertOperation(
		stellar.Operation{
			ID:                    "202",
			PagingToken:           "202",
			Type:                  "clawback_claimable_balance",
			TransactionHash:       "tx-clawback-balance",
			TransactionSuccessful: true,
			BalanceID:             "balance-3",
		},
		nil,
		&stellar.Transaction{FeeCharged: "100"},
		79,
		"ledger-hash",
		"202",
		123,
	)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, tx.FromAddress)
	assert.Empty(t, tx.ToAddress)

	var state stellarClaimableBalanceState
	found, err := kv.GetAny(idx.claimableBalanceStateKey("balance-3"), &state)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestStellarMainnetFetchAndParseTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	type stellarRealTxCase struct {
		name          string
		kind          string
		txHash        string
		operationID   string
		transferIndex string
		wantType      constant.TxType
		wantFrom      string
		wantTo        string
		wantAmount    string
		wantMetadata  map[string]string
		beforeConvert func(t *testing.T, idx *StellarIndexer, operation stellar.Operation, effects []stellar.Effect)
		verify        func(t *testing.T, tx types.Transaction, idx *StellarIndexer, payment *stellar.Payment, operation *stellar.Operation, effects []stellar.Effect)
	}

	testCases := []stellarRealTxCase{
		{
			name:          "native payment",
			kind:          "payment",
			txHash:        "81dba977244285fd3a6e9192ff2a594fe99d9bd6ec892533fe80c5f25c77c1f0",
			transferIndex: "265365546521460737",
			wantType:      constant.TxTypeNativeTransfer,
			wantFrom:      "GBC6NRTTQLRCABQHIR5J4R4YDJWFWRAO4ZRQIM2SVI5GSIZ2HZ42RINW",
			wantTo:        "GCZNOTQRRETQLBQH2MPWYMCLQBYMXKZI7XXYHS7F5RJHH7VMATQ57TQZ",
			wantAmount:    "120.0621870",
			verify: func(t *testing.T, tx types.Transaction, _ *StellarIndexer, _ *stellar.Payment, _ *stellar.Operation, _ []stellar.Effect) {
				assert.Equal(t, "", tx.AssetAddress)
			},
		},
		{
			name:          "payment with memo",
			kind:          "payment",
			txHash:        "5701567d4bcabbf54b43a084c0fc0b9fc544358d6b2aa70603ab0e6a9a8c2f81",
			transferIndex: "265854060396396545",
			wantType:      constant.TxTypeTokenTransfer,
			wantFrom:      "GBN32NH6TMWE4ZD4G245CF3UVOQRXD4FK3FDCLZ4DE5642HWFKSLLRMB",
			wantTo:        "GBKZQ2BYMOEJESIERVLWVSZD7CNNB7U2IMDCCWPDUN46AQAAXY3Y7I3C",
			wantAmount:    "18.8900000",
			verify: func(t *testing.T, tx types.Transaction, _ *StellarIndexer, payment *stellar.Payment, _ *stellar.Operation, _ []stellar.Effect) {
				require.NotNil(t, payment)
				assert.Equal(t, formatStellarAsset(payment.AssetIssuer, payment.AssetCode), tx.AssetAddress)
				assert.Equal(t, "pspb:4441859", tx.Memo)
				assert.Equal(t, types.MemoTypeText, tx.MemoType)
			},
		},
		{
			name:          "create account",
			kind:          "payment",
			txHash:        "981290ff3d4707fbb0413fd2e3aab41c359e19e626dcc3dfce5a88fc424937c6",
			transferIndex: "265348783263514625",
			wantType:      constant.TxTypeNativeTransfer,
			wantFrom:      "GDNHPXSFIZQMJJFBAFWUWG3442AHGI3WEWUYRYXIGMJRHDUQOPHTKLDC",
			wantTo:        "GBMB4C3RRTYWQZNTRZGKD4Z4OLMVRJZPFCQUY6UIRUACG7FLCYHAEGPN",
			wantAmount:    "1.5000000",
		},
		{
			name:          "payment",
			kind:          "payment",
			txHash:        "4003ae37c7b5126c364e6890a64792f57b268e1bebb43546e0347ba113d16530",
			transferIndex: "265336753060651009",
			wantType:      constant.TxTypeTokenTransfer,
			wantFrom:      "GAMV73I2KDYWDLGO5SNVL5IA6JSGLCALD7XJSXHPUB27MMJNTDQIX3UC",
			wantTo:        "GBN32NH6TMWE4ZD4G245CF3UVOQRXD4FK3FDCLZ4DE5642HWFKSLLRMB",
			wantAmount:    "56.6500000",
			verify: func(t *testing.T, tx types.Transaction, _ *StellarIndexer, payment *stellar.Payment, _ *stellar.Operation, _ []stellar.Effect) {
				require.NotNil(t, payment)
				assert.Equal(t, formatStellarAsset(payment.AssetIssuer, payment.AssetCode), tx.AssetAddress)
			},
		},
		{
			name:          "path payment strict send",
			kind:          "payment",
			txHash:        "9eeb46b6a608dde3985d1dd9d4110f030a6902c7ea0586de0d51cf52ce055b2c",
			transferIndex: "265348641529663489",
			wantType:      constant.TxTypeTokenTransfer,
			wantFrom:      "GBOA3PJZ3EUWLWLNXPICD57STTQSQKGEKDXNXC2VVPTML7SCQMAYUO3G",
			wantTo:        "GBOA3PJZ3EUWLWLNXPICD57STTQSQKGEKDXNXC2VVPTML7SCQMAYUO3G",
			wantAmount:    "1.9175151",
			verify: func(t *testing.T, tx types.Transaction, _ *StellarIndexer, payment *stellar.Payment, _ *stellar.Operation, _ []stellar.Effect) {
				require.NotNil(t, payment)
				assert.Equal(t, "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN:USDC", tx.AssetAddress)
			},
		},
		{
			name:          "create claimable balance",
			kind:          "operation",
			txHash:        "2308f9725aafde7aa6249e10abb1ccd3218116c6ee8d56305977db1cdc30319f",
			operationID:   "265336628506390529",
			transferIndex: "265336628506390529",
			wantType:      constant.TxTypeTokenTransfer,
			wantFrom:      "GCKIK5UOKKGXCDCWGY3BAKJ2T5H6BXOXHI4SMMEUSUE6NWLJP3F2KQUU",
			wantTo:        "",
			wantAmount:    "0.0010000",
			wantMetadata: map[string]string{
				types.MetadataKeySubtype:     "create_claimable_balance",
				types.MetadataKeyClaimableID: "000000009289e60febbca7863b48b8cf4d68b514d779ef13d1078b2fcd6d9371b0c3647a",
			},
			verify: func(t *testing.T, tx types.Transaction, idx *StellarIndexer, _ *stellar.Payment, operation *stellar.Operation, _ []stellar.Effect) {
				require.NotNil(t, operation)
				assert.Equal(t, "GBZH36ATUXJZKFRMQTAAW42MWNM34SOA4N6E7DQ62V3G5NVITC3QOVRL:OVRL", tx.AssetAddress)

				state, found, err := idx.loadClaimableBalanceState("000000009289e60febbca7863b48b8cf4d68b514d779ef13d1078b2fcd6d9371b0c3647a")
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, operation.Asset, state.Asset)
				assert.Equal(t, operation.Amount, state.Amount)
				assert.Equal(t, operation.SourceAccount, state.SourceAccount)
				assert.Equal(t, stellarClaimantAddresses(operation.Claimants), state.Claimants)
			},
		},
		{
			name:          "claim claimable balance",
			kind:          "operation",
			txHash:        "eaef80df07081e46288947f6e286f25c09ff552ab861b5f90a2cdfbd1976e880",
			operationID:   "265334914814349334",
			transferIndex: "265334914814349334",
			wantType:      constant.TxTypeTokenTransfer,
			wantFrom:      "",
			wantTo:        "GCKIK5UOKKGXCDCWGY3BAKJ2T5H6BXOXHI4SMMEUSUE6NWLJP3F2KQUU",
			wantMetadata: map[string]string{
				types.MetadataKeySubtype:     "claim_claimable_balance",
				types.MetadataKeyClaimableID: "0000000022b1a16b0a8d5aa67f932dc7a4f28f0ae44b7b270af4fce16a02362eea7e3897",
			},
			beforeConvert: func(t *testing.T, idx *StellarIndexer, operation stellar.Operation, effects []stellar.Effect) {
				effect := findStellarEffect(effects, "claimable_balance_claimed")
				require.NotNil(t, effect)
				require.NoError(t, idx.saveClaimableBalanceState("0000000022b1a16b0a8d5aa67f932dc7a4f28f0ae44b7b270af4fce16a02362eea7e3897", stellarClaimableBalanceState{
					Asset:         effect.Asset,
					Amount:        effect.Amount,
					SourceAccount: operation.Claimant,
					Claimants:     []string{operation.Claimant},
				}))
			},
			verify: func(t *testing.T, tx types.Transaction, idx *StellarIndexer, _ *stellar.Payment, _ *stellar.Operation, effects []stellar.Effect) {
				effect := findStellarEffect(effects, "claimable_balance_claimed")
				require.NotNil(t, effect)
				assert.Equal(t, effect.Amount, tx.Amount)
				assert.Equal(t, "GBZH36ATUXJZKFRMQTAAW42MWNM34SOA4N6E7DQ62V3G5NVITC3QOVRL:OVRL", tx.AssetAddress)

				_, found, err := idx.loadClaimableBalanceState("0000000022b1a16b0a8d5aa67f932dc7a4f28f0ae44b7b270af4fce16a02362eea7e3897")
				require.NoError(t, err)
				assert.False(t, found)
			},
		},
	}

	enabled := false
	for _, tc := range testCases {
		if tc.txHash != "" && tc.transferIndex != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		t.Skip("hardcoded real mainnet test cases are empty")
	}

	for _, tc := range testCases {
		tc := tc
		if tc.txHash == "" || tc.transferIndex == "" {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			kvstore := newMockKVStore()
			idx, client := newTestStellarLiveIndexer(t, kvstore)

			txDetail, err := client.GetTransaction(ctx, tc.txHash)
			require.NoError(t, err, "should fetch real mainnet transaction %s", tc.txHash)
			require.NotNil(t, txDetail)

			ledger, err := client.GetLedger(ctx, txDetail.Ledger)
			require.NoError(t, err, "should fetch real mainnet ledger %d", txDetail.Ledger)
			require.NotNil(t, ledger)

			ledgerTimestamp, err := parseRFC3339Unix(ledger.ClosedAt)
			require.NoError(t, err)

			var (
				tx        types.Transaction
				ok        bool
				payment   *stellar.Payment
				operation *stellar.Operation
				effects   []stellar.Effect
			)

			switch tc.kind {
			case "payment":
				payments, err := fetchStellarLedgerPayments(ctx, client, txDetail.Ledger)
				require.NoError(t, err)
				p := findStellarPayment(t, payments, tc.transferIndex)
				payment = &p
				tx, ok = idx.convertPayment(p, txDetail, ledger.Sequence, ledger.Hash, tc.transferIndex, ledgerTimestamp)
				require.True(t, ok)
			case "operation":
				operations, err := fetchStellarLedgerOperations(ctx, client, txDetail.Ledger)
				require.NoError(t, err)
				op := findStellarOperation(t, operations, tc.operationID)
				require.Equal(t, tc.transferIndex, op.PagingToken)
				operation = &op

				effects, err = fetchStellarOperationEffects(ctx, client, tc.operationID)
				require.NoError(t, err)

				if tc.beforeConvert != nil {
					tc.beforeConvert(t, idx, op, effects)
				}

				tx, ok, err = idx.convertOperation(op, effects, txDetail, ledger.Sequence, ledger.Hash, tc.transferIndex, ledgerTimestamp)
				require.NoError(t, err)
				require.True(t, ok)
			default:
				t.Fatalf("unsupported stellar mainnet test kind %q", tc.kind)
			}

			require.NotEmpty(t, tx.TxHash)
			require.NotEmpty(t, tx.Type)

			assert.Equal(t, tc.txHash, tx.TxHash)
			assert.Equal(t, tc.wantFrom, tx.FromAddress)
			assert.Equal(t, tc.wantTo, tx.ToAddress)
			if tc.wantAmount != "" {
				assert.Equal(t, tc.wantAmount, tx.Amount)
			}
			assert.Equal(t, tc.wantType, tx.Type)
			assert.Equal(t, ledger.Hash, tx.BlockHash)
			assert.Equal(t, tc.transferIndex, tx.TransferIndex)

			for key, want := range tc.wantMetadata {
				assert.Equal(t, want, tx.GetMetadataString(key), "metadata %s", key)
			}

			if tc.verify != nil {
				tc.verify(t, tx, idx, payment, operation, effects)
			}
		})
	}
}

func TestStellarMainnetBatchPaymentProducesMultipleTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	const batchTxHash = "5701567d4bcabbf54b43a084c0fc0b9fc544358d6b2aa70603ab0e6a9a8c2f81"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	idx, client := newTestStellarLiveIndexer(t, newMockKVStore())

	txDetail, err := client.GetTransaction(ctx, batchTxHash)
	require.NoError(t, err)
	require.NotNil(t, txDetail)

	ledger, err := client.GetLedger(ctx, txDetail.Ledger)
	require.NoError(t, err)
	require.NotNil(t, ledger)

	ledgerTimestamp, err := parseRFC3339Unix(ledger.ClosedAt)
	require.NoError(t, err)

	payments, err := fetchStellarLedgerPayments(ctx, client, txDetail.Ledger)
	require.NoError(t, err)

	converted := make([]types.Transaction, 0)
	toSet := make(map[string]struct{})
	transferIndexSet := make(map[string]struct{})
	for _, payment := range payments {
		if !strings.EqualFold(strings.TrimSpace(payment.TransactionHash), batchTxHash) {
			continue
		}
		transferIndex := strings.TrimSpace(payment.PagingToken)
		tx, ok := idx.convertPayment(payment, txDetail, ledger.Sequence, ledger.Hash, transferIndex, ledgerTimestamp)
		if !ok {
			continue
		}
		converted = append(converted, tx)
		if tx.ToAddress != "" {
			toSet[tx.ToAddress] = struct{}{}
		}
		if tx.TransferIndex != "" {
			transferIndexSet[tx.TransferIndex] = struct{}{}
		}
	}

	require.Greater(t, len(converted), 1, "batch tx should emit multiple transfers")
	require.Greater(t, len(toSet), 1, "batch tx should include multiple destination addresses")
	assert.Len(t, transferIndexSet, len(converted), "each emitted transfer should have unique transferIndex")

	for _, tx := range converted {
		assert.Equal(t, batchTxHash, tx.TxHash)
		assert.Equal(t, "pspb:4441859", tx.Memo)
		assert.Equal(t, types.MemoTypeText, tx.MemoType)
	}
}
