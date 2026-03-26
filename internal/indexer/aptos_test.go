package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/aptos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAptosPubkeyStore struct {
	addresses map[string]struct{}
}

const aptosMainnetRPC = "https://fullnode.mainnet.aptoslabs.com"

func (m mockAptosPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	_, ok := m.addresses[address]
	return ok
}

type aptosRealTransferFixture struct {
	name          string
	version       uint64
	txHash        string
	wantFee       string
	wantTransfers []aptosRealTransferOutput
}

type aptosRealTransferOutput struct {
	txType           constant.TxType
	from             string
	to               string
	amount           string
	asset            string
	rawTransferIndex string
}

func newTestAptosClient() *aptos.Client {
	return aptos.NewAptosClient(aptosMainnetRPC, nil, 30*time.Second, nil)
}

func newTestAptosIndexer() *AptosIndexer {
	return &AptosIndexer{
		chainName: "aptos_mainnet",
		config: config.ChainConfig{
			NetworkId: "aptos_mainnet",
		},
	}
}

func TestNormalizeAptosAsset_TreatsNativeMetadataAddressAsNative(t *testing.T) {
	asset := normalizeAptosAsset(aptosAssetInfo{
		TxType:       constant.TxTypeTokenTransfer,
		AssetAddress: "0xA",
	})

	assert.Equal(t, constant.TxTypeNativeTransfer, asset.TxType)
	assert.Empty(t, asset.AssetAddress)
}

func TestConvertAptosBlock_ParsesNativeTransferAndConvertsFeeToNative(t *testing.T) {
	idx := &AptosIndexer{
		chainName: "aptos_mainnet",
		config: config.ChainConfig{
			NetworkId: "aptos_mainnet",
		},
	}

	blockData := &aptos.BlockResponse{
		BlockHeight:    "10",
		BlockHash:      "0xblockhash",
		BlockTimestamp: "1735689600123456",
		Transactions: []aptos.Transaction{
			{
				Type:         "user_transaction",
				Hash:         "0xtxhash",
				Timestamp:    "1735689600222333",
				Success:      true,
				Sender:       "0x00000000000000000000000000000000000000000000000000000000000A11CE",
				GasUsed:      "5000",
				GasUnitPrice: "120",
				Events: []aptos.Event{
					coinEvent(
						t,
						"0x00000000000000000000000000000000000000000000000000000000000A11CE",
						"9",
						"11",
						"0x1::coin::WithdrawEvent",
						"1000000",
					),
					coinEvent(
						t,
						"0x0000000000000000000000000000000000000000000000000000000000000B0B",
						"8",
						"19",
						"0x1::coin::DepositEvent",
						"1000000",
					),
				},
				Changes: []aptos.WriteSetChange{
					coinStoreChange(t, "0xa11ce", "0x1::aptos_coin::AptosCoin", "2", "9"),
					coinStoreChange(t, "0xb0b", "0x1::aptos_coin::AptosCoin", "8", "3"),
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, 10)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "0xtxhash", tx.TxHash)
	assert.Equal(t, "aptos_mainnet", tx.NetworkId)
	assert.Equal(t, uint64(10), tx.BlockNumber)
	assert.Equal(t, "0xa11ce", tx.FromAddress)
	assert.Equal(t, "0xb0b", tx.ToAddress)
	assert.Equal(t, "1000000", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	assert.Equal(t, "", tx.AssetAddress)
	assert.Equal(t, "0xblockhash", tx.BlockHash)
	assert.Equal(t, "0.006", tx.TxFee.String())
	assert.Equal(t, uint64(1735689600), tx.Timestamp)
	assert.Equal(t, "0:0:seq:11:19:0:1", tx.TransferIndex)
}

func TestConvertAptosBlock_ParsesCoinTransferFromScriptPayload(t *testing.T) {
	idx := &AptosIndexer{
		chainName: "aptos_mainnet",
		config: config.ChainConfig{
			NetworkId: "aptos_mainnet",
		},
	}

	blockData := &aptos.BlockResponse{
		BlockHeight:    "22",
		BlockHash:      "0xblockhash2",
		BlockTimestamp: "1735689600000000",
		Transactions: []aptos.Transaction{
			{
				Type:         "user_transaction",
				Hash:         "0xtokenhash",
				Timestamp:    "1735689600555000",
				Success:      true,
				Sender:       "0x1",
				GasUsed:      "12",
				GasUnitPrice: "100",
				Payload: &aptos.TransactionPayload{
					Type: "script_payload",
				},
				Events: []aptos.Event{
					coinEvent(t, "0x1", "3", "0", "0x1::coin::WithdrawEvent", "42"),
					coinEvent(t, "0x2", "5", "1", "0x1::coin::DepositEvent", "42"),
				},
				Changes: []aptos.WriteSetChange{
					coinStoreChange(t, "0x1", "0xABCD::coin::USDC", "2", "3"),
					coinStoreChange(t, "0x2", "0xABCD::coin::USDC", "5", "4"),
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, 22)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "0xabcd::coin::usdc", tx.AssetAddress)
	assert.Equal(t, "42", tx.Amount)
	assert.Equal(t, "0.000012", tx.TxFee.String())
	assert.Equal(t, "0:0:seq:0:1:0:1", tx.TransferIndex)
}

func TestConvertAptosBlock_ParsesFungibleAssetTransferFromMultisigExecution(t *testing.T) {
	idx := &AptosIndexer{
		chainName: "aptos_testnet",
		config: config.ChainConfig{
			NetworkId: "aptos_testnet",
		},
	}

	blockData := &aptos.BlockResponse{
		BlockHeight:    "33",
		BlockHash:      "0xblockhash3",
		BlockTimestamp: "1772610461650250",
		Transactions: []aptos.Transaction{
			{
				Type:         "user_transaction",
				Hash:         "0x7174ba76d79ae15137615f6bce268c8c33f0a288b6e462d78af5731097215f5a",
				Timestamp:    "1772610461650250",
				Success:      true,
				Sender:       "0x3a7936eefc38e9578a86d9c7e06f24360982fed60e0e79a78b51da001c91cee7",
				GasUsed:      "572",
				GasUnitPrice: "100",
				Payload: &aptos.TransactionPayload{
					Type:     "entry_function_payload",
					Function: "0x1::multisig_account::execute_transaction",
				},
				Events: []aptos.Event{
					fungibleAssetEvent(t, "7", "0x1111", "0x1::fungible_asset::Withdraw", "500000"),
					fungibleAssetEvent(t, "8", "0x2222", "0x1::fungible_asset::Deposit", "500000"),
				},
				Changes: []aptos.WriteSetChange{
					objectCoreChange(t, "0x1111", "0x3a7936eefc38e9578a86d9c7e06f24360982fed60e0e79a78b51da001c91cee7"),
					objectCoreChange(t, "0x2222", "0xff26f441129a3727d21548cf080705700349b56e4ce616f07d80d87bb92bdb0c"),
					fungibleStoreChange(t, "0x1111", "0x69091fbab5f7d635ee7ac5098cf0c1efbe31d68fec0f2cd565e8d168daf52832"),
					fungibleStoreChange(t, "0x2222", "0x69091fbab5f7d635ee7ac5098cf0c1efbe31d68fec0f2cd565e8d168daf52832"),
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, 33)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "0x3a7936eefc38e9578a86d9c7e06f24360982fed60e0e79a78b51da001c91cee7", tx.FromAddress)
	assert.Equal(t, "0xff26f441129a3727d21548cf080705700349b56e4ce616f07d80d87bb92bdb0c", tx.ToAddress)
	assert.Equal(t, "500000", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "0x69091fbab5f7d635ee7ac5098cf0c1efbe31d68fec0f2cd565e8d168daf52832", tx.AssetAddress)
	assert.Equal(t, "0.000572", tx.TxFee.String())
	assert.Equal(t, "0:0:seq:7:8:0:1", tx.TransferIndex)
}

func TestConvertAptosBlock_ParsesBatchTransfersAsMultipleMovements(t *testing.T) {
	idx := &AptosIndexer{
		chainName: "aptos_mainnet",
		config: config.ChainConfig{
			NetworkId: "aptos_mainnet",
		},
	}

	blockData := &aptos.BlockResponse{
		BlockHeight:    "44",
		BlockHash:      "0xblockhash4",
		BlockTimestamp: "1735689601000000",
		Transactions: []aptos.Transaction{
			{
				Type:         "user_transaction",
				Hash:         "0xbatchhash",
				Timestamp:    "1735689601000000",
				Success:      true,
				Sender:       "0xa11ce",
				GasUsed:      "100",
				GasUnitPrice: "10",
				Payload: &aptos.TransactionPayload{
					Type:     "entry_function_payload",
					Function: "0x1::aptos_account::batch_transfer_coins",
				},
				Events: []aptos.Event{
					coinEvent(t, "0xa11ce", "9", "1", "0x1::coin::WithdrawEvent", "10"),
					coinEvent(t, "0xb0b", "8", "2", "0x1::coin::DepositEvent", "10"),
					coinEvent(t, "0xa11ce", "9", "3", "0x1::coin::WithdrawEvent", "20"),
					coinEvent(t, "0xcafe", "7", "4", "0x1::coin::DepositEvent", "20"),
				},
				Changes: []aptos.WriteSetChange{
					coinStoreChange(t, "0xa11ce", "0x1::aptos_coin::AptosCoin", "2", "9"),
					coinStoreChange(t, "0xb0b", "0x1::aptos_coin::AptosCoin", "8", "3"),
					coinStoreChange(t, "0xcafe", "0x1::aptos_coin::AptosCoin", "7", "4"),
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, 44)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 2)

	first := block.Transactions[0]
	second := block.Transactions[1]

	assert.Equal(t, "0xa11ce", first.FromAddress)
	assert.Equal(t, "0xb0b", first.ToAddress)
	assert.Equal(t, "10", first.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, first.Type)
	assert.Equal(t, "0:0:seq:1:2:0:1", first.TransferIndex)

	assert.Equal(t, "0xa11ce", second.FromAddress)
	assert.Equal(t, "0xcafe", second.ToAddress)
	assert.Equal(t, "20", second.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, second.Type)
	assert.Equal(t, "0:1:seq:3:4:2:3", second.TransferIndex)
}

func TestAptosMonitoredAddress_MatchesShortAndLongFormats(t *testing.T) {
	idx := &AptosIndexer{
		pubkeyStore: mockAptosPubkeyStore{
			addresses: map[string]struct{}{
				"0x00000000000000000000000000000000000000000000000000000000000000aa": {},
			},
		},
	}

	assert.True(t, idx.isMonitoredAddress("0xaa"))
	assert.True(t, idx.isMonitoredAddress("0x00000000000000000000000000000000000000000000000000000000000000AA"))
	assert.False(t, idx.isMonitoredAddress("0xbb"))
}

func TestConvertAptosBlock_PrefixesTransferIndexWithTxPosition(t *testing.T) {
	idx := &AptosIndexer{
		chainName: "aptos_mainnet",
		config: config.ChainConfig{
			NetworkId: "aptos_mainnet",
		},
	}

	blockData := &aptos.BlockResponse{
		BlockHeight:    "66",
		BlockHash:      "0xblockhash66",
		BlockTimestamp: "1735689600123456",
		Transactions: []aptos.Transaction{
			{
				Type:         "user_transaction",
				Hash:         "0xtx-one",
				Timestamp:    "1735689600222333",
				Success:      true,
				Sender:       "0xa11ce",
				GasUsed:      "1",
				GasUnitPrice: "1",
				Events: []aptos.Event{
					coinEvent(t, "0xa11ce", "9", "11", "0x1::coin::WithdrawEvent", "1"),
					coinEvent(t, "0xb0b", "8", "19", "0x1::coin::DepositEvent", "1"),
				},
				Changes: []aptos.WriteSetChange{
					coinStoreChange(t, "0xa11ce", "0x1::aptos_coin::AptosCoin", "2", "9"),
					coinStoreChange(t, "0xb0b", "0x1::aptos_coin::AptosCoin", "8", "3"),
				},
			},
			{
				Type:         "user_transaction",
				Hash:         "0xtx-two",
				Timestamp:    "1735689600222444",
				Success:      true,
				Sender:       "0xcafe",
				GasUsed:      "1",
				GasUnitPrice: "1",
				Events: []aptos.Event{
					coinEvent(t, "0xcafe", "7", "21", "0x1::coin::WithdrawEvent", "2"),
					coinEvent(t, "0xd00d", "6", "22", "0x1::coin::DepositEvent", "2"),
				},
				Changes: []aptos.WriteSetChange{
					coinStoreChange(t, "0xcafe", "0x1::aptos_coin::AptosCoin", "4", "7"),
					coinStoreChange(t, "0xd00d", "0x1::aptos_coin::AptosCoin", "6", "5"),
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, 66)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 2)
	assert.Equal(t, "0:0:seq:11:19:0:1", block.Transactions[0].TransferIndex)
	assert.Equal(t, "1:0:seq:21:22:0:1", block.Transactions[1].TransferIndex)
	assert.Equal(t, "0xblockhash66", block.Transactions[0].BlockHash)
	assert.Equal(t, "0xblockhash66", block.Transactions[1].BlockHash)
}

func TestAptosMainnetFetchAndParseTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := newTestAptosClient()
	idx := newTestAptosIndexer()

	testCases := []aptosRealTransferFixture{
		{
			name:    "primary fungible store transfer",
			version: 4677297376,
			txHash:  "0x834b588f580dee080bd36b0ccead00b73781b33afe9595a8c1d8866641971e0c",
			wantFee: "0.00507",
			wantTransfers: []aptosRealTransferOutput{
				{
					txType:           constant.TxTypeTokenTransfer,
					from:             "0xe175710417fdc335cfda20811011b79925abb9d26f1568833dfc64062ca023bd",
					to:               "0xae1a6f3d3daccaf77b55044cea133379934bba04a11b9d0bbd643eae5e6e9c70",
					amount:           "111000000",
					asset:            "0x357b0b74bc833e95a115ad22604854d6b0fca151cecd94111770e5d6ffc9dc2b",
					rawTransferIndex: "seq:0:0:0:1",
				},
			},
		},
		{
			name:    "custom module fungible asset transfer",
			version: 4677299560,
			txHash:  "0x0606953617ff069057834b98de83bad68247d8888ca849330d2d4c746cd881fc",
			wantFee: "0.005215",
			wantTransfers: []aptosRealTransferOutput{
				{
					txType:           constant.TxTypeTokenTransfer,
					from:             "0x3b3116a1094480310f84ca6017b855021a49faf92e24c54cbb217c103666d61b",
					to:               "0x5a96fab415f43721a44c5a761ecfcccc3dae9c21f34313f0e594b49d8d4564f4",
					amount:           "15",
					asset:            "0x7217ddb8006d44e945286edb847ef3c75c3c9ea5fb855f576baacac5c0edc239",
					rawTransferIndex: "seq:0:0:0:1",
				},
			},
		},
		{
			name:    "aptos account native transfer",
			version: 4677376519,
			txHash:  "0xc9a21616d74d8f0523242fa68222fe8a0817ea1867a53e1147a333b9bf05b962",
			wantFee: "0.00006363",
			wantTransfers: []aptosRealTransferOutput{
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0x36fb8eec9a46703803be12ebdfb096c76b50fd19b7d4ace25f693c1d3d6b925b",
					to:               "0xbabf76f1e6ac1af9c2666c07be12f90b188ed484fcef0bf1408961b5461373e9",
					amount:           "310023637",
					asset:            "",
					rawTransferIndex: "seq:0:0:0:1",
				},
			},
		},
		{
			name:    "coin transfer apt native",
			version: 4677354522,
			txHash:  "0xe812586e712c13b166f6023ce00a9351a9df5c056aec211eb55a7a662667aa15",
			wantFee: "0.000116",
			wantTransfers: []aptosRealTransferOutput{
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xa316b7d1fdd394698ab60a40c043fc4163dd421a61b6916eca2a25ea2784f04c",
					to:               "0xf597522b26d0f8c262834d736010c3f001b0fcfdd828eb1af2a151e75ed607c4",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:0:1",
				},
			},
		},
		{
			name:    "batch transfer coins multi recipient",
			version: 4677334780,
			txHash:  "0x4746d85426536fd1e1910a2bb273c2ae9f3d728a49fc1e1763b496fc6a2c6837",
			wantFee: "0.000458",
			wantTransfers: []aptosRealTransferOutput{
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xa76745246a56f713d48d1825dd0849e1c8a8298eeedce4ef1b6263109d0e547c",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:0:1",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0x283e703969872c0012c4719bb95760247abb840047643ddec7472c910bbaf3fc",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:2:3",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xe1c2321c8fd9a04d8e91783c7a6e6f6691ca9c55ec51c35081f953d41188b98e",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:4:5",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xcd8032ba416c61bee0425afa181ef031f48a1ac3a66dd3ce2769ed2229c58aa3",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:6:7",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xa14e7fa10096e68d16e5389ff351e36961c1aeb7038b4414319389280e19f2c6",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:8:9",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0x43696ed951b79264c4b63536e53774065019dde61a5784d3763e4e024a98c747",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:10:11",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xa392cfe21b93ca73f7416c9d191e293312f642b0dedb58fc0fedc354c0460bc0",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:12:13",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xac3af7afb3c71aaca3c962b6de165f24e7c476791c4db16faae8f47bc5f6e9ca",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:14:15",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0x27bad347df1cbacdbf3b28e9efb4437be00a519ee3e14806e162a584d55f8168",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:16:17",
				},
				{
					txType:           constant.TxTypeNativeTransfer,
					from:             "0xf04075549d2d842a726c96fb6d68661db44d62951ac97d3c807fe95f6e27fbf1",
					to:               "0xfe3c203c942fccb0987922fd9a4aac8899e3a1734557c92f6a50bd00f9f0cd7d",
					amount:           "1",
					asset:            "",
					rawTransferIndex: "seq:0:0:18:19",
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			blockData, err := client.GetBlockByVersion(ctx, tc.version, true)
			skipAptosRateLimit(t, err)
			require.NoError(t, err)
			require.NotNil(t, blockData)

			versionStr := strconv.FormatUint(tc.version, 10)
			txIndex := -1
			for i, tx := range blockData.Transactions {
				if tx.Version == versionStr {
					txIndex = i
					require.Equal(t, tc.txHash, tx.Hash)
					break
				}
			}
			require.NotEqual(t, -1, txIndex, "target version %d should exist in block payload", tc.version)

			blockHeight, err := strconv.ParseUint(blockData.BlockHeight, 10, 64)
			require.NoError(t, err)

			block, err := idx.convertBlock(blockData, 0)
			require.NoError(t, err)

			gotTransfers := make([]types.Transaction, 0, len(tc.wantTransfers))
			for _, tx := range block.Transactions {
				if tx.TxHash == tc.txHash {
					gotTransfers = append(gotTransfers, tx)
				}
			}

			require.Len(t, gotTransfers, len(tc.wantTransfers))
			for i, want := range tc.wantTransfers {
				tx := gotTransfers[i]
				assert.Equal(t, tc.txHash, tx.TxHash)
				assert.Equal(t, "aptos_mainnet", tx.NetworkId)
				assert.Equal(t, blockHeight, tx.BlockNumber)
				assert.Equal(t, blockData.BlockHash, tx.BlockHash)
				assert.Equal(t, want.from, tx.FromAddress)
				assert.Equal(t, want.to, tx.ToAddress)
				assert.Equal(t, want.amount, tx.Amount)
				assert.Equal(t, want.txType, tx.Type)
				assert.Equal(t, want.asset, tx.AssetAddress)
				assert.Equal(t, tc.wantFee, tx.TxFee.String())
				assert.Equal(t, fmt.Sprintf("%d:%d:%s", txIndex, i, want.rawTransferIndex), tx.TransferIndex)
				assert.NotZero(t, tx.Timestamp)
			}
		})
	}
}

func skipAptosRateLimit(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "http 429") {
		t.Skipf("aptos rpc rate limited: %v", err)
	}
}

func coinEvent(
	t *testing.T,
	account string,
	creation string,
	sequence string,
	eventType string,
	amount string,
) aptos.Event {
	t.Helper()
	return aptos.Event{
		GUID: aptos.EventGUID{
			CreationNumber: creation,
			AccountAddress: account,
		},
		SequenceNumber: sequence,
		Type:           eventType,
		Data: aptos.EventData{
			"amount": mustRawJSON(t, amount),
		},
	}
}

func fungibleAssetEvent(
	t *testing.T,
	sequence string,
	store string,
	eventType string,
	amount string,
) aptos.Event {
	t.Helper()
	return aptos.Event{
		GUID: aptos.EventGUID{
			CreationNumber: "0",
			AccountAddress: "0x0",
		},
		SequenceNumber: sequence,
		Type:           eventType,
		Data: aptos.EventData{
			"store":  mustRawJSON(t, store),
			"amount": mustRawJSON(t, amount),
		},
	}
}

func coinStoreChange(
	t *testing.T,
	account string,
	coinType string,
	depositCreation string,
	withdrawCreation string,
) aptos.WriteSetChange {
	t.Helper()

	data := map[string]json.RawMessage{}
	if depositCreation != "" {
		data["deposit_events"] = mustRawJSON(t, map[string]any{
			"guid": map[string]any{
				"id": map[string]any{
					"addr":         account,
					"creation_num": depositCreation,
				},
			},
		})
	}
	if withdrawCreation != "" {
		data["withdraw_events"] = mustRawJSON(t, map[string]any{
			"guid": map[string]any{
				"id": map[string]any{
					"addr":         account,
					"creation_num": withdrawCreation,
				},
			},
		})
	}

	return aptos.WriteSetChange{
		Type:    "write_resource",
		Address: account,
		Data: &aptos.WriteSetResource{
			Type: fmt.Sprintf("0x1::coin::CoinStore<%s>", coinType),
			Data: data,
		},
	}
}

func objectCoreChange(t *testing.T, objectAddress, owner string) aptos.WriteSetChange {
	t.Helper()
	return aptos.WriteSetChange{
		Type:    "write_resource",
		Address: objectAddress,
		Data: &aptos.WriteSetResource{
			Type: "0x1::object::ObjectCore",
			Data: map[string]json.RawMessage{
				"owner": mustRawJSON(t, owner),
			},
		},
	}
}

func fungibleStoreChange(t *testing.T, storeAddress, metadataAddress string) aptos.WriteSetChange {
	t.Helper()
	return aptos.WriteSetChange{
		Type:    "write_resource",
		Address: storeAddress,
		Data: &aptos.WriteSetResource{
			Type: "0x1::fungible_asset::FungibleStore",
			Data: map[string]json.RawMessage{
				"metadata": mustRawJSON(t, map[string]any{
					"inner": metadataAddress,
				}),
			},
		},
	}
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()

	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}
