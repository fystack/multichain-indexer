package indexer

import (
	"encoding/base64"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc/cosmos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCosmosConvertBlock_ParsesTransfersAndFee(t *testing.T) {
	txPayload := []byte("tx-one")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "osmosis_mainnet",
		config: config.ChainConfig{
			NetworkId:   "osmosis-1",
			NativeDenom: "uosmo",
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "100",
				Time:   "2026-02-13T00:00:00Z",
				LastBlockID: cosmos.LastBlockID{
					Hash: "PARENT_HASH",
				},
			},
			Data: cosmos.BlockData{
				Txs: []string{txEncoded},
			},
		},
	}

	blockResults := &cosmos.BlockResultsResponse{
		Height: "100",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: b64("sender"), Value: b64("osmo1sender")},
							{Key: b64("recipient"), Value: b64("osmo1recipient")},
							{Key: b64("amount"), Value: b64("123uosmo,45ibc/ABCDEF")},
						},
					},
					{
						Type: "tx",
						Attributes: []cosmos.EventAttribute{
							{Key: b64("fee"), Value: b64("7uosmo")},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 2)

	assert.Equal(t, uint64(100), block.Number)
	assert.Equal(t, "BLOCK_HASH", block.Hash)
	assert.Equal(t, "PARENT_HASH", block.ParentHash)
	assert.Equal(t, uint64(1770940800), block.Timestamp)

	expectedHash := hashCosmosTxs([]string{txEncoded})[0]

	nativeTx := block.Transactions[0]
	assert.Equal(t, expectedHash, nativeTx.TxHash)
	assert.Equal(t, "osmosis-1", nativeTx.NetworkId)
	assert.Equal(t, "osmo1sender", nativeTx.FromAddress)
	assert.Equal(t, "osmo1recipient", nativeTx.ToAddress)
	assert.Equal(t, "123", nativeTx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, nativeTx.Type)
	assert.Equal(t, "", nativeTx.AssetAddress)
	assert.Equal(t, "7", nativeTx.TxFee.String())
	assert.Equal(t, uint64(1), nativeTx.Confirmations)
	assert.Equal(t, types.StatusConfirmed, nativeTx.Status)

	tokenTx := block.Transactions[1]
	assert.Equal(t, expectedHash, tokenTx.TxHash)
	assert.Equal(t, "45", tokenTx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tokenTx.Type)
	assert.Equal(t, "ibc/ABCDEF", tokenTx.AssetAddress)
	assert.Equal(t, "0", tokenTx.TxFee.String())
}

func TestExtractCosmosTransfers_PlainEventAttributes(t *testing.T) {
	events := []cosmos.Event{
		{
			Type: "transfer",
			Attributes: []cosmos.EventAttribute{
				{Key: "sender", Value: "osmo1a"},
				{Key: "recipient", Value: "osmo1b"},
				{Key: "amount", Value: "100uosmo"},
			},
		},
	}

	transfers := extractCosmosTransfers(events)
	require.Len(t, transfers, 1)
	assert.Equal(t, "osmo1a", transfers[0].sender)
	assert.Equal(t, "osmo1b", transfers[0].recipient)
	assert.Equal(t, "100", transfers[0].amount)
	assert.Equal(t, "uosmo", transfers[0].denom)
}

func TestCosmosClassifyDenom_UsesConfiguredNativeDenom(t *testing.T) {
	idx := &CosmosIndexer{
		chainName: "celestia_mainnet",
		config: config.ChainConfig{
			NetworkId:   "celestia",
			NativeDenom: "utia",
		},
	}

	txType, asset := idx.classifyDenom("utia")
	assert.Equal(t, constant.TxTypeNativeTransfer, txType)
	assert.Equal(t, "", asset)

	txType, asset = idx.classifyDenom("ibc/XYZ")
	assert.Equal(t, constant.TxTypeTokenTransfer, txType)
	assert.Equal(t, "ibc/XYZ", asset)
}

func TestCosmosClassifyDenom_InfersCosmosHubNativeDenom(t *testing.T) {
	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId: "cosmoshub-4",
		},
	}

	txType, asset := idx.classifyDenom("uatom")
	assert.Equal(t, constant.TxTypeNativeTransfer, txType)
	assert.Equal(t, "", asset)

	txType, asset = idx.classifyDenom("ibc/ATOM")
	assert.Equal(t, constant.TxTypeTokenTransfer, txType)
	assert.Equal(t, "ibc/ATOM", asset)
}

func TestCosmosClassifyDenom_InfersCosmosHubFromCosmos1Hint(t *testing.T) {
	idx := &CosmosIndexer{
		chainName: "cosmos1_hub_mainnet",
		config: config.ChainConfig{
			NetworkId: "mainnet",
		},
	}

	txType, asset := idx.classifyDenom("uatom")
	assert.Equal(t, constant.TxTypeNativeTransfer, txType)
	assert.Equal(t, "", asset)
}

func TestCosmosConvertBlock_SupportsCosmosHubAddresses(t *testing.T) {
	txPayload := []byte("tx-cosmos-hub")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId: "cosmoshub-4",
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "200",
				Time:   "2026-02-20T00:00:00Z",
				LastBlockID: cosmos.LastBlockID{
					Hash: "PARENT_HASH",
				},
			},
			Data: cosmos.BlockData{
				Txs: []string{txEncoded},
			},
		},
	}

	blockResults := &cosmos.BlockResultsResponse{
		Height: "200",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1recipient"},
							{Key: "amount", Value: "999uatom"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "cosmos1sender", tx.FromAddress)
	assert.Equal(t, "cosmos1recipient", tx.ToAddress)
	assert.Equal(t, "999", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	assert.Equal(t, "", tx.AssetAddress)
}

func b64(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}
