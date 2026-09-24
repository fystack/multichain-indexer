package indexer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/xrp"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
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

func (m mockXRPPubkeyStore) ExistBatch(_ enum.NetworkType, addresses []string) []bool {
	results := make([]bool, len(addresses))
	for i, addr := range addresses {
		_, results[i] = m.addresses[addr]
	}
	return results
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

const xrpMainnetRPC = "https://xrplcluster.com"

func newTestXRPLiveIndexer(t *testing.T) (*XRPIndexer, *xrp.Client) {
	t.Helper()

	client := xrp.NewClient(xrpMainnetRPC, nil, 30*time.Second, nil)
	t.Cleanup(func() {
		_ = client.Close()
	})

	failover := rpc.NewFailover[xrp.XRPLAPI](nil)
	require.NoError(t, failover.AddProvider(&rpc.Provider{
		Name:       "xrp-mainnet",
		URL:        xrpMainnetRPC,
		Network:    "xrp_mainnet",
		ClientType: rpc.ClientTypeRPC,
		Client:     client,
		State:      rpc.StateHealthy,
	}))

	return NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		failover,
		nil,
	), client
}

func findXRPBlockTransaction(t *testing.T, block *types.Block, txHash string) types.Transaction {
	t.Helper()

	for _, tx := range block.Transactions {
		if tx.TxHash == txHash {
			return tx
		}
	}

	t.Fatalf("transaction %s not found in block %d", txHash, block.Number)
	return types.Transaction{}
}

func findXRPLedgerTransactionIndex(t *testing.T, ledger *xrp.Ledger, txHash string) string {
	t.Helper()

	for i, tx := range ledger.Transactions {
		if tx.Hash == txHash {
			return fmt.Sprintf("%d", i)
		}
	}

	t.Fatalf("transaction %s not found in ledger %s", txHash, ledger.LedgerIndex.String())
	return ""
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
	assert.Equal(t, "LEDGER_HASH", nativeTx.BlockHash)
	assert.Equal(t, "0", nativeTx.TransferIndex)
	destinationTagValue := nativeTx.DestinationTag
	assert.Equal(t, "778899", destinationTagValue)
	assert.Equal(t, "0.000012", nativeTx.TxFee.String())

	tokenTx := block.Transactions[1]
	assert.Equal(t, "TX_TOKEN", tokenTx.TxHash)
	assert.Equal(t, constant.TxTypeTokenTransfer, tokenTx.Type)
	assert.Equal(t, "5.20", tokenTx.Amount)
	assert.Equal(t, "rIssuer:USD", tokenTx.AssetAddress)
	assert.Equal(t, "LEDGER_HASH", tokenTx.BlockHash)
	assert.Equal(t, "1", tokenTx.TransferIndex)
}

func TestXRPConvertPaymentTransaction_StoresSourceSideRoutingForPathPayment(t *testing.T) {
	t.Parallel()

	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rDest": {}}},
	)

	tx, ok := idx.convertPaymentTransaction(
		xrp.Transaction{
			Hash:            "TX_PATH",
			Account:         "rSender",
			Destination:     "rDest",
			TransactionType: "Payment",
			Fee:             "12",
			Amount:          "5000000",
		},
		&xrp.Meta{
			TransactionResult: "tesSUCCESS",
			DeliveredAmount:   "5000000",
			AffectedNodes: []xrp.Node{
				{
					ModifiedNode: &xrp.LedgerNode{
						LedgerEntryType: "RippleState",
						FinalFields: &xrp.LedgerFields{
							Balance: map[string]any{
								"currency": "USD",
								"value":    "90",
							},
							LowLimit: map[string]any{
								"currency": "USD",
								"issuer":   "rSender",
								"value":    "1000000000",
							},
							HighLimit: map[string]any{
								"currency": "USD",
								"issuer":   "rIssuer",
								"value":    "0",
							},
						},
						PreviousFields: &xrp.LedgerFields{
							Balance: map[string]any{
								"currency": "USD",
								"value":    "100",
							},
						},
					},
				},
			},
		},
		105,
		"LEDGER_HASH",
		0,
		123,
	)
	require.True(t, ok)
	assert.Equal(t, "5", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	assert.Equal(t, "", tx.AssetAddress)
	assert.Equal(t, string(constant.TxTypeTokenTransfer), tx.GetMetadataString(metadataKeySourceTxType))
	assert.Equal(t, "10", tx.GetMetadataString(metadataKeySourceAmount))
	assert.Equal(t, "rIssuer:USD", tx.GetMetadataString(metadataKeySourceAsset))

	outTx := idx.NormalizeForDirection(tx, types.DirectionOut)
	assert.Equal(t, constant.TxTypeTokenTransfer, outTx.Type)
	assert.Equal(t, "10", outTx.Amount)
	assert.Equal(t, "rIssuer:USD", outTx.AssetAddress)
	assert.Equal(t, "", outTx.GetMetadataString(metadataKeySourceTxType))
	assert.Equal(t, "", outTx.GetMetadataString(metadataKeySourceAmount))
	assert.Equal(t, "", outTx.GetMetadataString(metadataKeySourceAsset))
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

func TestXRPConvertLedger_ParsesAccountDelete(t *testing.T) {
	t.Parallel()

	destinationTag := uint32(42)
	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rDest": {}}},
	)

	block, err := idx.convertLedger(&xrp.Ledger{
		LedgerHash:  "LEDGER_HASH",
		ParentHash:  "PARENT_HASH",
		LedgerIndex: "101",
		CloseTime:   100,
		Transactions: []xrp.Transaction{
			{
				Hash:            "TX_DELETE",
				Account:         "rSource",
				Destination:     "rDest",
				DestinationTag:  &destinationTag,
				TransactionType: "AccountDelete",
				Fee:             "5000000",
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
					AffectedNodes: []xrp.Node{
						{
							ModifiedNode: &xrp.LedgerNode{
								LedgerEntryType: "AccountRoot",
								FinalFields: &xrp.LedgerFields{
									Account: "rDest",
									Balance: "15000000",
								},
								PreviousFields: &xrp.LedgerFields{
									Balance: "10000000",
								},
							},
						},
					},
				},
			},
		},
	}, 101)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "rSource", tx.FromAddress)
	assert.Equal(t, "rDest", tx.ToAddress)
	assert.Equal(t, "5", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	subtype := tx.GetMetadataString(types.MetadataKeySubtype)
	assert.Equal(t, "account_delete", subtype)
	tag := tx.DestinationTag
	assert.Equal(t, "42", tag)
}

func TestXRPConvertLedger_ParsesCheckCashIssuedAsset(t *testing.T) {
	t.Parallel()

	destinationTag := uint32(778899)
	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rCashier": {}}},
	)

	block, err := idx.convertLedger(&xrp.Ledger{
		LedgerHash:  "LEDGER_HASH",
		ParentHash:  "PARENT_HASH",
		LedgerIndex: "102",
		CloseTime:   100,
		Transactions: []xrp.Transaction{
			{
				Hash:            "TX_CHECK",
				Account:         "rCashier",
				CheckID:         "CHECK1",
				TransactionType: "CheckCash",
				Fee:             "12",
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
					DeliveredAmount: map[string]any{
						"currency": "USD",
						"issuer":   "rIssuer",
						"value":    "5.20",
					},
					AffectedNodes: []xrp.Node{
						{
							DeletedNode: &xrp.LedgerNode{
								LedgerEntryType: "Check",
								LedgerIndex:     "CHECK1",
								FinalFields: &xrp.LedgerFields{
									Account:        "rSource",
									Destination:    "rCashier",
									DestinationTag: &destinationTag,
								},
							},
						},
					},
				},
			},
		},
	}, 102)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "rSource", tx.FromAddress)
	assert.Equal(t, "rCashier", tx.ToAddress)
	assert.Equal(t, "5.20", tx.Amount)
	assert.Equal(t, "rIssuer:USD", tx.AssetAddress)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	subtype := tx.GetMetadataString(types.MetadataKeySubtype)
	assert.Equal(t, "check_cash", subtype)
	checkID := tx.GetMetadataString(types.MetadataKeyCheckID)
	assert.Equal(t, "CHECK1", checkID)
	tag := tx.DestinationTag
	assert.Equal(t, "778899", tag)
}

func TestXRPConvertLedger_ParsesEscrowFinish(t *testing.T) {
	t.Parallel()

	destinationTag := uint32(12)
	offerSequence := uint32(7)
	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rDest": {}}},
	)

	block, err := idx.convertLedger(&xrp.Ledger{
		LedgerHash:  "LEDGER_HASH",
		ParentHash:  "PARENT_HASH",
		LedgerIndex: "103",
		CloseTime:   100,
		Transactions: []xrp.Transaction{
			{
				Hash:            "TX_ESCROW",
				Account:         "rExecutor",
				Owner:           "rOwner",
				OfferSequence:   &offerSequence,
				TransactionType: "EscrowFinish",
				Fee:             "12",
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
					AffectedNodes: []xrp.Node{
						{
							DeletedNode: &xrp.LedgerNode{
								LedgerEntryType: "Escrow",
								FinalFields: &xrp.LedgerFields{
									Account:        "rOwner",
									Destination:    "rDest",
									DestinationTag: &destinationTag,
									Amount:         "2500000",
								},
							},
						},
					},
				},
			},
		},
	}, 103)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "rOwner", tx.FromAddress)
	assert.Equal(t, "rDest", tx.ToAddress)
	assert.Equal(t, "2.5", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	subtype := tx.GetMetadataString(types.MetadataKeySubtype)
	assert.Equal(t, "escrow_finish", subtype)
	escrowOwner := tx.GetMetadataString(types.MetadataKeyEscrowOwner)
	assert.Equal(t, "rOwner", escrowOwner)
	escrowSequence := tx.GetMetadataString(types.MetadataKeyEscrowSequence)
	assert.Equal(t, "7", escrowSequence)
	tag := tx.DestinationTag
	assert.Equal(t, "12", tag)
}

func TestXRPConvertLedger_ParsesClawback(t *testing.T) {
	t.Parallel()

	idx := NewXRPIndexer(
		"xrp_mainnet",
		config.ChainConfig{NetworkId: "xrp_mainnet"},
		nil,
		mockXRPPubkeyStore{addresses: map[string]struct{}{"rHolder": {}}},
	)

	block, err := idx.convertLedger(&xrp.Ledger{
		LedgerHash:  "LEDGER_HASH",
		ParentHash:  "PARENT_HASH",
		LedgerIndex: "104",
		CloseTime:   100,
		Transactions: []xrp.Transaction{
			{
				Hash:            "TX_CLAWBACK",
				Account:         "rIssuer",
				TransactionType: "Clawback",
				Fee:             "12",
				Amount: map[string]any{
					"currency": "USD",
					"issuer":   "rHolder",
					"value":    "5.20",
				},
				Meta: &xrp.Meta{
					TransactionResult: "tesSUCCESS",
				},
			},
		},
	}, 104)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "rHolder", tx.FromAddress)
	assert.Equal(t, xrpBurnAddress, tx.ToAddress)
	assert.Equal(t, "rIssuer:USD", tx.AssetAddress)
	assert.Equal(t, "5.20", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	subtype := tx.GetMetadataString(types.MetadataKeySubtype)
	assert.Equal(t, "clawback", subtype)
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

func TestXRPMainnetFetchAndParseTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	type xrpRealTxCase struct {
		name         string
		ledgerIndex  uint64
		txHash       string
		wantType     constant.TxType
		wantFrom     string
		wantTo       string
		wantAmount   string
		wantAsset    string
		wantDestinationTag string
		wantMetadata map[string]string
	}

	testCases := []xrpRealTxCase{
		{
			name:        "native payment with destination tag",
			ledgerIndex: 103233614,
			txHash:      "F13A325DF5544E7E6F17108B18D0E8198EC161EE26E66345E09E346F154BDD17",
			wantType:    constant.TxTypeNativeTransfer,
			wantFrom:    "rMgUZD9LBBrsSLXmAe2naCAWGtTg5KmsT1",
			wantTo:      "rvEBMEWUUcKNMvXhxzo6Db1pFdrgfi34B",
			wantAmount:  "8.999987",
			wantDestinationTag: "1862722728",
		},
		{
			name:        "issued payment",
			ledgerIndex: 94229458,
			txHash:      "DF31D6B64DFB75600591235BA000B2D2C00C6989A84AB1DD596B5CB9FA8D109B",
			wantType:    constant.TxTypeTokenTransfer,
			wantFrom:    "rXPMxDRxMM6JLk8AMVh569iap3TtnjaF3",
			wantTo:      "rG3cUwFd6UrZocrEDm17b8YZeaHGaNmJBg",
			wantAmount:  "815",
			wantAsset:   "rG4kgkvbAiy69t6keLzSBGTgRk8hgJM7Go:4245415652000000000000000000000000000000",
		},
		{
			name:        "account delete",
			ledgerIndex: 103056689,
			txHash:      "BDB1F216067FDC16CA70E585900C72CE75450B1D2524269137C2C4CE59CFA5D5",
			wantType:    constant.TxTypeNativeTransfer,
			wantFrom:    "rauksASXjkCF13cpjDGA1DCgWWRm4arJu4",
			wantTo:      "rMkJJ4HHHBRdeHvE2UXx1xPDh4hnuRW9TZ",
			wantAmount:  "0.899292",
			wantDestinationTag: "1717742333",
			wantMetadata: map[string]string{
				types.MetadataKeySubtype: "account_delete",
			},
		},
		{
			name:        "check cash",
			ledgerIndex: 103056610,
			txHash:      "9317A1D9C13A618B8BE74A22D846B6AD70D62364900E8DAA720D79221CFCDF5B",
			wantType:    constant.TxTypeNativeTransfer,
			wantFrom:    "rNsvWWTquJ2rWhGcTEx1AEoWte7ZD9VQPe",
			wantTo:      "ra61uRmrMG1hpVQKhkL6pMwbJqdYcK1FT7",
			wantAmount:  "4.004482",
			wantMetadata: map[string]string{
				types.MetadataKeySubtype: "check_cash",
				types.MetadataKeyCheckID: "3C00D77DE88D4C6AA8E3844AA00D68347925D2CB4ABC987E65AAB9D53CDEB4F0",
			},
		},
		{
			name:        "escrow finish",
			ledgerIndex: 100398890,
			txHash:      "2861C22727E48831F8D45E304F15631C8927D52E46E6ED5060929FE2C7A83A34",
			wantType:    constant.TxTypeNativeTransfer,
			wantFrom:    "rw37cgPnjt57zqKgFuwqZExsiq79A3kZuP",
			wantTo:      "raftCiiYJoU5UrkBKjAoHfLuo6iZDotLr6",
			wantAmount:  "58.974",
			wantMetadata: map[string]string{
				types.MetadataKeySubtype:        "escrow_finish",
				types.MetadataKeyEscrowOwner:    "rw37cgPnjt57zqKgFuwqZExsiq79A3kZuP",
				types.MetadataKeyEscrowSequence: "76046814",
			},
		},
		{
			name:        "clawback",
			ledgerIndex: 102983492,
			txHash:      "ADCF519C04EB8AC273EE1C136258D8A62833E2E57AE3A56EB9C6595EB272FB05",
			wantType:    constant.TxTypeTokenTransfer,
			wantFrom:    "rLqBaPeR8xe4d1RJ7tBg6iFVyJjpo1rS2T",
			wantTo:      xrpBurnAddress,
			wantAmount:  "200",
			wantAsset:   "r4WspQvEcvCjLQz6cDAxTS5vV6rQ9GuKsF:CLW",
			wantMetadata: map[string]string{
				types.MetadataKeySubtype: "clawback",
			},
		},
	}

	enabled := false
	for _, tc := range testCases {
		if tc.ledgerIndex != 0 && tc.txHash != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		t.Skip("hardcoded real mainnet test cases are empty")
	}

	idx, client := newTestXRPLiveIndexer(t)

	for _, tc := range testCases {
		tc := tc
		if tc.ledgerIndex == 0 || tc.txHash == "" {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			ledger, err := client.GetLedgerByIndex(ctx, tc.ledgerIndex)
			require.NoError(t, err, "should fetch real mainnet ledger %d", tc.ledgerIndex)
			require.NotNil(t, ledger)

			block, err := idx.GetBlock(ctx, tc.ledgerIndex)
			require.NoError(t, err, "should parse real mainnet ledger %d", tc.ledgerIndex)
			require.NotNil(t, block)

			tx := findXRPBlockTransaction(t, block, tc.txHash)
			require.NotEmpty(t, tx.TxHash)
			require.NotEmpty(t, tx.FromAddress)
			require.NotEmpty(t, tx.ToAddress)
			require.NotEmpty(t, tx.Type)

			assert.Equal(t, tc.txHash, tx.TxHash)
			assert.Equal(t, tc.wantFrom, tx.FromAddress)
			assert.Equal(t, tc.wantTo, tx.ToAddress)
			assert.Equal(t, tc.wantAmount, tx.Amount)
			assert.Equal(t, tc.wantAsset, tx.AssetAddress)
			assert.Equal(t, tc.wantType, tx.Type)
			assert.Equal(t, block.Hash, tx.BlockHash)
			assert.Equal(t, findXRPLedgerTransactionIndex(t, ledger, tc.txHash), tx.TransferIndex)
			assert.Equal(t, tc.wantDestinationTag, tx.DestinationTag)

			for key, want := range tc.wantMetadata {
				assert.Equal(t, want, tx.GetMetadataString(key), "metadata %s", key)
			}
		})
	}
}
