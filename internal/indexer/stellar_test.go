package indexer

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/stellar"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
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
	latest       uint64
	ledgers      map[uint64]*stellar.Ledger
	pages        map[uint64]map[string]*stellar.PaymentsPage
	transactions map[string]*stellar.Transaction
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
func (m *mockStellarAPI) GetTransaction(_ context.Context, hash string) (*stellar.Transaction, error) {
	return m.transactions[hash], nil
}

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
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GDEST": {}}},
	)

	block, err := idx.GetBlock(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 4)

	nativeTx := block.Transactions[0]
	assert.Equal(t, constant.TxTypeNativeTransfer, nativeTx.Type)
	assert.Equal(t, "", nativeTx.AssetAddress)
	assert.Equal(t, "memo-1", nativeTx.Memo)
	assert.Equal(t, "text", nativeTx.MemoType)
	assert.Equal(t, "0.00001", nativeTx.TxFee.String())

	tokenTx := block.Transactions[1]
	assert.Equal(t, constant.TxTypeTokenTransfer, tokenTx.Type)
	assert.Equal(t, "GISSUER:USDC", tokenTx.AssetAddress)
	assert.Equal(t, "memo-2", tokenTx.Memo)
	assert.Equal(t, "99.2500000", tokenTx.Amount)

	createTx := block.Transactions[2]
	assert.Equal(t, constant.TxTypeNativeTransfer, createTx.Type)
	assert.Equal(t, "GCREATOR", createTx.FromAddress)
	assert.Equal(t, "GDEST", createTx.ToAddress)
	assert.Equal(t, "10000.0000000", createTx.Amount)

	pathTx := block.Transactions[3]
	assert.Equal(t, constant.TxTypeNativeTransfer, pathTx.Type)
	assert.Equal(t, "GPATHFROM", pathTx.FromAddress)
	assert.Equal(t, "GDEST", pathTx.ToAddress)
	assert.Equal(t, "7.1250000", pathTx.Amount)
	assert.Equal(t, "memo-3", pathTx.Memo)
	assert.Equal(t, "0.00003", pathTx.TxFee.String())
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
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GMONITORED": {}}},
	)
	_, ok := idx.convertPayment(payment, nil, 12, 1)
	require.False(t, ok)

	idx = NewStellarIndexer(
		"stellar_mainnet",
		config.ChainConfig{NetworkId: "stellar_mainnet", TwoWayIndexing: true},
		nil,
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GMONITORED": {}}},
	)
	_, ok = idx.convertPayment(payment, nil, 12, 1)
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
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GNEW": {}}},
	)

	tx, ok := idx.convertPayment(payment, nil, 42, 123)
	require.True(t, ok)
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
		mockStellarPubkeyStore{addresses: map[string]struct{}{"GDEST": {}}},
	)

	tx, ok := idx.convertPayment(payment, nil, 52, 123)
	require.True(t, ok)
	assert.Equal(t, "GMERGED", tx.FromAddress)
	assert.Equal(t, "GDEST", tx.ToAddress)
	assert.Equal(t, "8.5000000", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
}
