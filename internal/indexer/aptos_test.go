package indexer

import (
	"encoding/json"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc/aptos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAptosPubkeyStore struct {
	addresses map[string]struct{}
}

func (m mockAptosPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	_, ok := m.addresses[address]
	return ok
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
				Payload: &aptos.TransactionPayload{
					Type:     "entry_function_payload",
					Function: "0x1::aptos_account::transfer",
					Arguments: []json.RawMessage{
						mustRawJSON(t, "0x000000000000000000000000000000000000000000000000000000000000B0B"),
						mustRawJSON(t, "1000000"),
					},
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
	assert.Equal(t, "0.006", tx.TxFee.String())
	assert.Equal(t, uint64(1735689600), tx.Timestamp)
}

func TestConvertAptosBlock_ParsesTokenTransfer(t *testing.T) {
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
					Type:          "entry_function_payload",
					Function:      "0x1::coin::transfer",
					TypeArguments: []string{"0xABCD::coin::USDC"},
					Arguments: []json.RawMessage{
						mustRawJSON(t, "0x2"),
						mustRawJSON(t, "42"),
					},
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
}

func TestConvertAptosBlock_ParsesFungibleAssetTransfer(t *testing.T) {
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
					Function: "0x1::aptos_account::transfer_fungible_assets",
					Arguments: []json.RawMessage{
						mustRawJSON(t, map[string]string{
							"inner": "0x69091fbab5f7d635ee7ac5098cf0c1efbe31d68fec0f2cd565e8d168daf52832",
						}),
						mustRawJSON(t, "0xff26f441129a3727d21548cf080705700349b56e4ce616f07d80d87bb92bdb0c"),
						mustRawJSON(t, "500000"),
					},
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

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()

	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}
