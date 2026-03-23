package indexer

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/cosmos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockCosmosPubkeyStore struct {
	addresses map[string]struct{}
}

func (m mockCosmosPubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	_, ok := m.addresses[address]
	return ok
}

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

	txHashes, err := hashCosmosTxs([]string{txEncoded})
	require.NoError(t, err)
	expectedHash := txHashes[0]

	nativeTx := block.Transactions[0]
	assert.Equal(t, expectedHash, nativeTx.TxHash)
	assert.Equal(t, "osmosis-1", nativeTx.NetworkId)
	assert.Equal(t, "osmo1sender", nativeTx.FromAddress)
	assert.Equal(t, "osmo1recipient", nativeTx.ToAddress)
	assert.Equal(t, "123", nativeTx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, nativeTx.Type)
	assert.Equal(t, "", nativeTx.AssetAddress)
	assert.Equal(t, "0.000007", nativeTx.TxFee.String())
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

func TestDecodeCosmosEventValue_LeavesPlainTextThatLooksLikeBase64(t *testing.T) {
	assert.Equal(t, "transfer", decodeCosmosEventValue("transfer"))
	assert.Equal(t, "sender", decodeCosmosEventValue("sender"))
}

func TestDecodeCosmosEventValue_DecodesBase64Text(t *testing.T) {
	assert.Equal(t, "sender", decodeCosmosEventValue("c2VuZGVy"))
	assert.Equal(t, "recipient", decodeCosmosEventValue("cmVjaXBpZW50"))
}

func TestExtractCosmosFee_PrefersNativeDenomAndNormalizesMicro(t *testing.T) {
	events := []cosmos.Event{
		{
			Type: "tx",
			Attributes: []cosmos.EventAttribute{
				{Key: "fee", Value: "100stake,42uatom"},
			},
		},
	}

	fee := extractCosmosFee(events, "uatom")
	assert.Equal(t, "0.000042", fee.String())
}

func TestHashCosmosTxs_ReturnsErrorForInvalidBase64(t *testing.T) {
	_, err := hashCosmosTxs([]string{"not-base64*"})
	require.Error(t, err)
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

func TestCosmosClassifyDenom_WithoutNativeDenomTreatsAsToken(t *testing.T) {
	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId: "cosmoshub-4",
		},
	}

	txType, asset := idx.classifyDenom("uatom")
	assert.Equal(t, constant.TxTypeTokenTransfer, txType)
	assert.Equal(t, "uatom", asset)
}

func TestCosmosNativeDenom_ReturnsTrimmedConfiguredValue(t *testing.T) {
	idx := &CosmosIndexer{
		chainName: "cosmos1_hub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "mainnet",
			NativeDenom: "  uatom  ",
		},
	}

	assert.Equal(t, "uatom", idx.nativeDenom())
}

func TestCosmosConvertBlock_SupportsCosmosHubAddresses(t *testing.T) {
	txPayload := []byte("tx-cosmos-hub")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "cosmoshub-4",
			NativeDenom: "uatom",
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

func TestCosmosConvertBlock_SkipsSourceIBCTransferAndFeePay(t *testing.T) {
	txPayload := []byte("tx-ibc-transfer")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "cosmoshub-4",
			NativeDenom: "uatom",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"cosmos1ibcreceiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "300",
				Time:   "2026-02-24T00:00:00Z",
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
		Height: "300",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "message",
						Attributes: []cosmos.EventAttribute{
							{Key: "action", Value: "/ibc.applications.transfer.v1.MsgTransfer"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1escrow"},
							{Key: "amount", Value: "250000uusdc"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "ibc_transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "receiver", Value: "cosmos1ibcreceiver"},
							{Key: "amount", Value: "250000"},
							{Key: "denom", Value: "uusdc"},
						},
					},
					{
						Type: "send_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"cosmos1ibcreceiver\",\"sender\":\"cosmos1sender\"}"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "fee_pay",
						Attributes: []cosmos.EventAttribute{
							{Key: "fee", Value: "127572stake"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 0)
}

func TestCosmosConvertBlock_ParsesRecvPacketTransfer(t *testing.T) {
	txPayload := []byte("tx-ibc-recv-packet")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "osmosis_mainnet",
		config: config.ChainConfig{
			NetworkId:   "osmosis-1",
			NativeDenom: "uosmo",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"osmo1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "301",
				Time:   "2026-02-24T00:00:01Z",
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
		Height: "301",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "recv_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"osmo1receiver\",\"sender\":\"cosmos1sender\"}"},
							{Key: "packet_src_port", Value: "transfer"},
							{Key: "packet_src_channel", Value: "channel-0"},
							{Key: "packet_dst_port", Value: "transfer"},
							{Key: "packet_dst_channel", Value: "channel-7"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "write_acknowledgement",
						Attributes: []cosmos.EventAttribute{
							{Key: "msg_index", Value: "0"},
							{Key: "packet_ack", Value: "{\"result\":\"AQ==\"}"},
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
	assert.Equal(t, "osmo1receiver", tx.ToAddress)
	assert.Equal(t, "250000", tx.Amount)
	assert.Equal(
		t,
		deriveCosmosRecvDenomFromPacket("uusdc", "transfer", "channel-0", "transfer", "channel-7"),
		tx.AssetAddress,
	)
}

func TestCosmosConvertBlock_DedupsRecvPacketWithBankTransfer(t *testing.T) {
	txPayload := []byte("tx-ibc-recv-with-bank")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmos_hub_local",
		config: config.ChainConfig{
			NetworkId:   "provider-local",
			NativeDenom: "stake",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"cosmos1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "305",
				Time:   "2026-02-24T00:00:05Z",
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
		Height: "305",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1escrowmodule"},
							{Key: "recipient", Value: "cosmos1receiver"},
							{Key: "amount", Value: "250000uusdc"},
						},
					},
					{
						Type: "recv_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"cosmos1receiver\",\"sender\":\"cosmos1sourceuser\"}"},
							{Key: "packet_src_port", Value: "transfer"},
							{Key: "packet_src_channel", Value: "channel-0"},
							{Key: "packet_dst_port", Value: "transfer"},
							{Key: "packet_dst_channel", Value: "channel-0"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "write_acknowledgement",
						Attributes: []cosmos.EventAttribute{
							{Key: "msg_index", Value: "0"},
							{Key: "packet_ack", Value: "{\"result\":\"AQ==\"}"},
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
	assert.Equal(t, "cosmos1sourceuser", tx.FromAddress)
	assert.Equal(t, "cosmos1receiver", tx.ToAddress)
	assert.Equal(t, "250000", tx.Amount)
	assert.Equal(
		t,
		deriveCosmosRecvDenomFromPacket("uusdc", "transfer", "channel-0", "transfer", "channel-0"),
		tx.AssetAddress,
	)
}

func TestCosmosConvertBlock_KeepNativeTransferInIBCSendTx(t *testing.T) {
	txPayload := []byte("tx-ibc-send-with-native")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmos_hub_local",
		config: config.ChainConfig{
			NetworkId:   "provider-local",
			NativeDenom: "stake",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"cosmos1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "306",
				Time:   "2026-02-24T00:00:06Z",
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
		Height: "306",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "message",
						Attributes: []cosmos.EventAttribute{
							{Key: "action", Value: "/ibc.applications.transfer.v1.MsgTransfer"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1receiver"},
							{Key: "amount", Value: "100stake,250000uusdc"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "ibc_transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "receiver", Value: "osmo1receiver"},
							{Key: "amount", Value: "250000"},
							{Key: "denom", Value: "uusdc"},
						},
					},
					{
						Type: "send_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"osmo1receiver\",\"sender\":\"cosmos1sender\"}"},
							{Key: "msg_index", Value: "0"},
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
	assert.Equal(t, "cosmos1receiver", tx.ToAddress)
	assert.Equal(t, "100", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	assert.Equal(t, "", tx.AssetAddress)
}

func TestCosmosConvertBlock_ParsesCW20Transfer(t *testing.T) {
	txPayload := []byte("tx-cw20-transfer")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmos_hub_local",
		config: config.ChainConfig{
			NetworkId:   "provider-local",
			NativeDenom: "stake",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"cosmos1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "307",
				Time:   "2026-02-24T00:00:07Z",
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
		Height: "307",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "wasm",
						Attributes: []cosmos.EventAttribute{
							{Key: "_contract_address", Value: "cosmos1cw20contract"},
							{Key: "action", Value: "transfer"},
							{Key: "from", Value: "cosmos1sender"},
							{Key: "to", Value: "cosmos1receiver"},
							{Key: "amount", Value: "250000"},
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
	assert.Equal(t, "cosmos1receiver", tx.ToAddress)
	assert.Equal(t, "250000", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "cosmos1cw20contract", tx.AssetAddress)
}

func TestCosmosConvertBlock_ParsesCW20TransferBase64Attrs(t *testing.T) {
	txPayload := []byte("tx-cw20-transfer-b64")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmos_hub_local",
		config: config.ChainConfig{
			NetworkId:   "provider-local",
			NativeDenom: "stake",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"cosmos1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "308",
				Time:   "2026-02-24T00:00:08Z",
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
		Height: "308",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "wasm",
						Attributes: []cosmos.EventAttribute{
							{Key: b64("_contract_address"), Value: b64("cosmos1cw20contract")},
							{Key: b64("action"), Value: b64("transfer_from")},
							{Key: b64("from"), Value: b64("cosmos1sender")},
							{Key: b64("recipient"), Value: b64("cosmos1receiver")},
							{Key: b64("amount"), Value: b64("123")},
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
	assert.Equal(t, "cosmos1receiver", tx.ToAddress)
	assert.Equal(t, "123", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	assert.Equal(t, "cosmos1cw20contract", tx.AssetAddress)
}

func TestCosmosConvertBlock_SkipsRecvPacketWhenAckFailed(t *testing.T) {
	txPayload := []byte("tx-ibc-recv-failed")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "osmosis_mainnet",
		config: config.ChainConfig{
			NetworkId:   "osmosis-1",
			NativeDenom: "uosmo",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"osmo1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "302",
				Time:   "2026-02-24T00:00:02Z",
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
		Height: "302",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "recv_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"osmo1receiver\",\"sender\":\"cosmos1sender\"}"},
							{Key: "packet_src_port", Value: "transfer"},
							{Key: "packet_src_channel", Value: "channel-0"},
							{Key: "packet_dst_port", Value: "transfer"},
							{Key: "packet_dst_channel", Value: "channel-7"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "write_acknowledgement",
						Attributes: []cosmos.EventAttribute{
							{Key: "msg_index", Value: "0"},
							{Key: "packet_ack", Value: "{\"error\":\"failed\"}"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 0)
}

func TestCosmosConvertBlock_SkipsSendPacketForMsgTransfer(t *testing.T) {
	txPayload := []byte("tx-ibc-send-packet")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "cosmoshub-4",
			NativeDenom: "uatom",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"osmo1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "303",
				Time:   "2026-02-24T00:00:03Z",
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
		Height: "303",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "message",
						Attributes: []cosmos.EventAttribute{
							{Key: "action", Value: "/ibc.applications.transfer.v1.MsgTransfer"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "send_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"osmo1receiver\",\"sender\":\"cosmos1sender\"}"},
							{Key: "msg_index", Value: "0"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 0)
}

func TestCosmosConvertBlock_SkipsSendPacketWhenNotMsgTransfer(t *testing.T) {
	txPayload := []byte("tx-non-ibc-send-packet")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "cosmoshub-4",
			NativeDenom: "uatom",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"osmo1receiver": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "304",
				Time:   "2026-02-24T00:00:04Z",
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
		Height: "304",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "message",
						Attributes: []cosmos.EventAttribute{
							{Key: "action", Value: "/ibc.core.channel.v1.MsgChannelOpenInit"},
							{Key: "msg_index", Value: "0"},
						},
					},
					{
						Type: "send_packet",
						Attributes: []cosmos.EventAttribute{
							{Key: "packet_data", Value: "{\"amount\":\"250000\",\"denom\":\"uusdc\",\"receiver\":\"osmo1receiver\",\"sender\":\"cosmos1sender\"}"},
							{Key: "msg_index", Value: "0"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 0)
}

func TestExtractCosmosTransfers_BatchTransferEvent(t *testing.T) {
	events := []cosmos.Event{
		{
			Type: "transfer",
			Attributes: []cosmos.EventAttribute{
				{Key: "sender", Value: "cosmos1sender"},
				{Key: "recipient", Value: "cosmos1recipienta"},
				{Key: "amount", Value: "100uatom"},
				{Key: "sender", Value: "cosmos1sender"},
				{Key: "recipient", Value: "cosmos1recipientb"},
				{Key: "amount", Value: "250uatom,7ibc/XYZ"},
			},
		},
	}

	transfers := extractCosmosTransfers(events)
	require.Len(t, transfers, 3)

	assert.Equal(t, "cosmos1sender", transfers[0].sender)
	assert.Equal(t, "cosmos1recipienta", transfers[0].recipient)
	assert.Equal(t, "100", transfers[0].amount)
	assert.Equal(t, "uatom", transfers[0].denom)

	assert.Equal(t, "cosmos1sender", transfers[1].sender)
	assert.Equal(t, "cosmos1recipientb", transfers[1].recipient)
	assert.Equal(t, "250", transfers[1].amount)
	assert.Equal(t, "uatom", transfers[1].denom)

	assert.Equal(t, "cosmos1sender", transfers[2].sender)
	assert.Equal(t, "cosmos1recipientb", transfers[2].recipient)
	assert.Equal(t, "7", transfers[2].amount)
	assert.Equal(t, "ibc/XYZ", transfers[2].denom)
}

func TestCosmosConvertBlock_SetsBlockHashAndTransferIndex(t *testing.T) {
	txPayload := []byte("tx-blockhash-transferindex")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "cosmoshub-4",
			NativeDenom: "uatom",
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "310",
				Time:   "2026-02-24T00:00:10Z",
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
		Height: "310",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1recipienta"},
							{Key: "amount", Value: "100uatom"},
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1recipientb"},
							{Key: "amount", Value: "7ibc/XYZ"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.Len(t, block.Transactions, 2)
	assert.Equal(t, "BLOCK_HASH", block.Transactions[0].BlockHash)
	assert.Equal(t, "0:0", block.Transactions[0].TransferIndex)
	assert.Equal(t, "BLOCK_HASH", block.Transactions[1].BlockHash)
	assert.Equal(t, "0:1", block.Transactions[1].TransferIndex)
}

func TestCosmosConvertBlock_ParsesBatchTransferEvent(t *testing.T) {
	txPayload := []byte("tx-batch-transfer")
	txEncoded := base64.StdEncoding.EncodeToString(txPayload)

	idx := &CosmosIndexer{
		chainName: "cosmoshub_mainnet",
		config: config.ChainConfig{
			NetworkId:   "cosmoshub-4",
			NativeDenom: "uatom",
		},
		pubkeyStore: mockCosmosPubkeyStore{
			addresses: map[string]struct{}{
				"cosmos1recipienta": {},
				"cosmos1recipientb": {},
			},
		},
	}

	blockData := &cosmos.BlockResponse{
		BlockID: cosmos.BlockID{Hash: "BLOCK_HASH"},
		Block: cosmos.Block{
			Header: cosmos.BlockHeader{
				Height: "309",
				Time:   "2026-02-24T00:00:09Z",
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
		Height: "309",
		TxsResults: []cosmos.TxResult{
			{
				Code: 0,
				Events: []cosmos.Event{
					{
						Type: "transfer",
						Attributes: []cosmos.EventAttribute{
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1recipienta"},
							{Key: "amount", Value: "100uatom"},
							{Key: "sender", Value: "cosmos1sender"},
							{Key: "recipient", Value: "cosmos1recipientb"},
							{Key: "amount", Value: "250uatom,7ibc/XYZ"},
						},
					},
					{
						Type: "tx",
						Attributes: []cosmos.EventAttribute{
							{Key: "fee", Value: "42uatom"},
						},
					},
				},
			},
		},
	}

	block, err := idx.convertBlock(blockData, blockResults)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 3)

	txHashes, err := hashCosmosTxs([]string{txEncoded})
	require.NoError(t, err)
	expectedHash := txHashes[0]

	first := block.Transactions[0]
	assert.Equal(t, expectedHash, first.TxHash)
	assert.Equal(t, "cosmos1sender", first.FromAddress)
	assert.Equal(t, "cosmos1recipienta", first.ToAddress)
	assert.Equal(t, "100", first.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, first.Type)
	assert.Equal(t, "", first.AssetAddress)
	assert.Equal(t, "0.000042", first.TxFee.String())

	second := block.Transactions[1]
	assert.Equal(t, expectedHash, second.TxHash)
	assert.Equal(t, "cosmos1sender", second.FromAddress)
	assert.Equal(t, "cosmos1recipientb", second.ToAddress)
	assert.Equal(t, "250", second.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, second.Type)
	assert.Equal(t, "", second.AssetAddress)
	assert.True(t, second.TxFee.IsZero())

	third := block.Transactions[2]
	assert.Equal(t, expectedHash, third.TxHash)
	assert.Equal(t, "cosmos1sender", third.FromAddress)
	assert.Equal(t, "cosmos1recipientb", third.ToAddress)
	assert.Equal(t, "7", third.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, third.Type)
	assert.Equal(t, "ibc/XYZ", third.AssetAddress)
	assert.True(t, third.TxFee.IsZero())
}

func TestCosmosMainnetFetchAndParseTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	type cosmosRealTxCase struct {
		name        string
		rpcURL      string
		height      uint64
		txHash      string
		chainName   string
		networkID   string
		nativeDenom string
		wantType    constant.TxType
	}

	// Fill these with real Cosmos mainnet transactions.
	// The parser currently works block-first, so each case needs:
	// - rpcURL: chain RPC endpoint
	// - height: exact block height containing the tx
	// - txHash: expected tx hash inside the parsed block output
	testCases := []cosmosRealTxCase{
		{
			name:        "cosmoshub native transfer",
			rpcURL:      "https://cosmos-rpc.publicnode.com",
			height:      30296492,
			txHash:      "10C90080AF071736C71F55130D672CB885DAC12963C4319D005420EF580706B7",
			chainName:   "cosmoshub_mainnet",
			networkID:   "cosmoshub-4",
			nativeDenom: "uatom",
			wantType:    constant.TxTypeNativeTransfer,
		},
		{
			name:        "osmosis ibc or token transfer",
			rpcURL:      "https://cosmos-rpc.publicnode.com",
			chainName:   "cosmoshub_mainnet",
			height:      30296490,
			txHash:      "A3264C9CA23F78669D4EF6C0ADC3178AF08F9B74E555B1E0480A716FF264B74E",
			networkID:   "cosmoshub-4",
			nativeDenom: "uatom",
			wantType:    constant.TxTypeTokenTransfer,
		},
		{
			name:        "recv packet transfer",
			rpcURL:      "https://cosmos-rpc.publicnode.com",
			chainName:   "cosmoshub_mainnet",
			height:      30297381,
			txHash:      "69F40A5F4E9BD58D5A5F30DBC4C53086D9080E703E77A44543807978207706B8",
			networkID:   "cosmoshub-4",
			nativeDenom: "uatom",
			wantType:    constant.TxTypeTokenTransfer,
		},
	}

	enabled := false
	for _, tc := range testCases {
		if tc.height != 0 && strings.TrimSpace(tc.txHash) != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		t.Skip("hardcoded real mainnet test cases are empty")
	}

	for _, tc := range testCases {
		tc := tc
		if tc.height == 0 || strings.TrimSpace(tc.txHash) == "" {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			client := cosmos.NewCosmosClient(tc.rpcURL, nil, 15*time.Second, nil)
			idx := &CosmosIndexer{
				chainName: tc.chainName,
				config: config.ChainConfig{
					NetworkId:   tc.networkID,
					NativeDenom: tc.nativeDenom,
				},
			}

			blockData, err := client.GetBlock(ctx, tc.height)
			require.NoError(t, err, "should fetch real mainnet block %d", tc.height)
			require.NotNil(t, blockData)

			blockResults, err := client.GetBlockResults(ctx, tc.height)
			require.NoError(t, err, "should fetch real mainnet block_results %d", tc.height)
			require.NotNil(t, blockResults)

			block, err := idx.convertBlock(blockData, blockResults)
			require.NoError(t, err)
			require.NotNil(t, block)
			require.NotEmpty(t, block.Transactions, "should parse at least one semantic transfer from block")

			wantHash := strings.ToUpper(strings.TrimSpace(tc.txHash))
			found := false
			foundWantedType := false
			for _, item := range block.Transactions {
				if item.TxHash != wantHash {
					continue
				}
				found = true
				if item.Type == tc.wantType {
					foundWantedType = true
				}
				require.Contains(t, []constant.TxType{
					constant.TxTypeNativeTransfer,
					constant.TxTypeTokenTransfer,
				}, item.Type)
				require.NotEmpty(t, item.FromAddress)
				require.NotEmpty(t, item.ToAddress)
				require.NotEmpty(t, item.Amount)
			}

			require.True(t, found, "expected tx hash %s in parsed block output", wantHash)
			require.True(t, foundWantedType, "expected tx type %s for tx %s", tc.wantType, wantHash)
		})
	}
}

func b64(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}
