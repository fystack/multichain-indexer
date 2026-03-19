package indexer

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/xrp"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockXRPPubkeyStore struct {
	addresses map[string]struct{}
}

func (m mockXRPPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	_, ok := m.addresses[address]
	return ok
}

type mockXRPAPI struct {
	ledgers map[uint64]*xrp.Ledger
}

var _ xrp.XRPLAPI = (*mockXRPAPI)(nil)

func (m *mockXRPAPI) CallRPC(context.Context, string, any) (*rpc.RPCResponse, error) { return nil, nil }
func (m *mockXRPAPI) Do(context.Context, string, string, any, map[string]string) ([]byte, error) {
	return nil, nil
}
func (m *mockXRPAPI) GetNetworkType() string { return rpc.NetworkXRP }
func (m *mockXRPAPI) GetClientType() string  { return rpc.ClientTypeRPC }
func (m *mockXRPAPI) GetURL() string         { return "http://xrpl.test" }
func (m *mockXRPAPI) Close() error           { return nil }

func (m *mockXRPAPI) GetLatestLedgerIndex(context.Context) (uint64, error) { return 100, nil }
func (m *mockXRPAPI) GetLedgerByIndex(_ context.Context, ledgerIndex uint64) (*xrp.Ledger, error) {
	return m.ledgers[ledgerIndex], nil
}
func (m *mockXRPAPI) BatchGetLedgersByIndex(_ context.Context, ledgerIndexes []uint64) (map[uint64]*xrp.Ledger, error) {
	out := make(map[uint64]*xrp.Ledger, len(ledgerIndexes))
	for _, ledgerIndex := range ledgerIndexes {
		out[ledgerIndex] = m.ledgers[ledgerIndex]
	}
	return out, nil
}

func TestXRPConvertLedger_ParsesNativeAndIssuedPayments(t *testing.T) {
	t.Parallel()

	destinationTag := uint32(778899)
	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{
			NetworkId: "xrp_mainnet",
		},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{
			"rDest": {},
		}},
	)

	ledger := &xrp.Ledger{
		LedgerHash:  "LEDGER_HASH",
		ParentHash:  "PARENT_HASH",
		LedgerIndex: "100",
		CloseTime:   100,
		Transactions: []xrp.Transaction{
			{
				Hash:            "TX_NATIVE",
				Account:         "rSource",
				Destination:     "rDest",
				DestinationTag:  &destinationTag,
				TransactionType: "Payment",
				Fee:             "12",
				Amount:          "2500000",
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
				},
			},
			{
				Hash:            "TX_TOKEN",
				Account:         "rIssuerSource",
				Destination:     "rDest",
				TransactionType: "Payment",
				Fee:             "20",
				Amount: map[string]any{
					"currency": "USD",
					"issuer":   "rIssuer",
					"value":    "5.25",
				},
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
					DeliveredAmount: map[string]any{
						"currency": "USD",
						"issuer":   "rIssuer",
						"value":    "5.20",
					},
				},
			},
			{
				Hash:            "TX_SKIP",
				Account:         "rSource",
				Destination:     "rDest",
				TransactionType: "OfferCreate",
				Fee:             "10",
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
				},
			},
		},
	}

	block, err := idx.convertLedger(ledger, 100)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 2)

	nativeTx := block.Transactions[0]
	assert.Equal(t, "TX_NATIVE", nativeTx.TxHash)
	assert.Equal(t, "2.5", nativeTx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, nativeTx.Type)
	assert.Equal(t, "", nativeTx.AssetAddress)
	assert.Equal(t, "778899", nativeTx.DestinationTag)
	assert.Equal(t, "0.000012", nativeTx.TxFee.String())

	tokenTx := block.Transactions[1]
	assert.Equal(t, "TX_TOKEN", tokenTx.TxHash)
	assert.Equal(t, constant.TxTypeTokenTransfer, tokenTx.Type)
	assert.Equal(t, "5.20", tokenTx.Amount)
	assert.Equal(t, "rIssuer:USD", tokenTx.AssetAddress)
}

func TestXRPConvertLedger_RespectsTwoWayIndexing(t *testing.T) {
	t.Parallel()

	ledger := &xrp.Ledger{
		LedgerHash:  "LEDGER_HASH",
		ParentHash:  "PARENT_HASH",
		LedgerIndex: "5",
		CloseTime:   100,
		Transactions: []xrp.Transaction{
			{
				Hash:            "TX_OUT",
				Account:         "rMonitored",
				Destination:     "rOther",
				TransactionType: "Payment",
				Fee:             "12",
				Amount:          "1000000",
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
				},
			},
		},
	}

	idxWithoutTwoWay := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rMonitored": {}}},
	)
	block, err := idxWithoutTwoWay.convertLedger(ledger, 5)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 0)

	idxWithTwoWay := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{
			NetworkId:      "xrp_mainnet",
			TwoWayIndexing: true,
		},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rMonitored": {}}},
	)
	block, err = idxWithTwoWay.convertLedger(ledger, 5)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)
}

func TestXRPGetBlocksByNumbers_PreservesInputOrder(t *testing.T) {
	t.Parallel()

	api := &mockXRPAPI{
		ledgers: map[uint64]*xrp.Ledger{
			7: {LedgerHash: "L7", ParentHash: "L6", LedgerIndex: "7", CloseTime: 10},
			3: {LedgerHash: "L3", ParentHash: "L2", LedgerIndex: "3", CloseTime: 10},
		},
	}
	failover := rpc.NewFailover[xrp.XRPLAPI](nil)
	require.NoError(t, failover.AddProvider(&rpc.Provider{
		Name:       "xrp-test",
		URL:        "http://xrpl.test",
		Network:    "xrp_mainnet",
		ClientType: rpc.ClientTypeRPC,
		Client:     api,
		State:      rpc.StateHealthy,
	}))

	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet", Throttle: config.Throttle{Concurrency: 2}},
		failover,
		nil,
	)

	results, err := idx.GetBlocksByNumbers(context.Background(), []uint64{7, 3})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, uint64(7), results[0].Number)
	assert.Equal(t, "L7", results[0].Block.Hash)
	assert.Equal(t, uint64(3), results[1].Number)
	assert.Equal(t, "L3", results[1].Block.Hash)
}
