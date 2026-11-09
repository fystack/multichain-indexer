package indexer

import (
	"encoding/json"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc/tron"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mainnet TRC-20 deposit b48e7145…2ca2 in block 87831636, as returned by TronGrid
// getblockbynum (trimmed to this tx) and gettransactioninfobyid.
const (
	tronMainnetTxID      = "b48e714533282851e5edc6e0d3023cfb830e00bd321078927d9820c560d22ca2"
	tronMainnetBlockID   = "00000000053c345470f2d5063d273caed8aa408c6fd49f3f9c8286c413fa64bc"
	tronMainnetFrom      = "TSwPPq4Ho59JJ8NN1ra7ZnP38jaYJe3vG8"
	tronMainnetTo        = "THvqco7LAEUmH4eytEUS1o6aVsi7unfNGw"
	tronMainnetNetworkID = "tron-mainnet"

	tronMainnetBlockJSON = `{
  "blockID": "00000000053c345470f2d5063d273caed8aa408c6fd49f3f9c8286c413fa64bc",
  "block_header": {
    "raw_data": {
      "number": 87831636,
      "timestamp": 1794168651000,
      "parentHash": "00000000053c345347b2da1f03c67ba74ec37bdcc1d8ca0b80d600797b5d90a0"
    }
  },
  "transactions": [
    {
      "txID": "b48e714533282851e5edc6e0d3023cfb830e00bd321078927d9820c560d22ca2",
      "raw_data": {
        "contract": [
          {
            "parameter": {
              "value": {
                "owner_address": "41ba21fb50b479029a74f1436b55b224fa096674e8",
                "contract_address": "41a614f803b6fd780986a42c78ec9c7f77e6ded13c",
                "data": "a9059cbb000000000000000000000000574e52f24239f252275fdb8d64f42d71e0b9a05d0000000000000000000000000000000000000000000000000000000000e51c67"
              }
            },
            "type": "TriggerSmartContract"
          }
        ],
        "timestamp": 1794168630750
      },
      "ret": [{"contractRet": "SUCCESS"}]
    }
  ]
}`

	tronMainnetTxInfoJSON = `{
  "id": "b48e714533282851e5edc6e0d3023cfb830e00bd321078927d9820c560d22ca2",
  "fee": 345000,
  "blockNumber": 87831636,
  "blockTimeStamp": 1794168651000,
  "receipt": {
    "energy_usage": 64285,
    "energy_usage_total": 64285,
    "net_fee": 345000,
    "result": "SUCCESS",
    "energy_penalty_total": 49635
  },
  "log": [
    {
      "address": "a614f803b6fd780986a42c78ec9c7f77e6ded13c",
      "topics": [
        "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
        "000000000000000000000000ba21fb50b479029a74f1436b55b224fa096674e8",
        "000000000000000000000000574e52f24239f252275fdb8d64f42d71e0b9a05d"
      ],
      "data": "0000000000000000000000000000000000000000000000000000000000e51c67"
    }
  ]
}`
)

type tronPubkeyStoreStub map[string]struct{}

func (s tronPubkeyStoreStub) Exist(_ enum.NetworkType, address string) bool {
	_, ok := s[address]
	return ok
}

func (s tronPubkeyStoreStub) ExistBatch(addressType enum.NetworkType, addresses []string) []bool {
	out := make([]bool, len(addresses))
	for i, addr := range addresses {
		out[i] = s.Exist(addressType, addr)
	}
	return out
}

func loadTronMainnetFixture(t *testing.T) (*tron.Block, *tron.TxnInfo) {
	t.Helper()
	var block tron.Block
	require.NoError(t, json.Unmarshal([]byte(tronMainnetBlockJSON), &block))
	var info tron.TxnInfo
	require.NoError(t, json.Unmarshal([]byte(tronMainnetTxInfoJSON), &info))
	return &block, &info
}

func newTestTronIndexer(watched ...string) *TronIndexer {
	store := tronPubkeyStoreStub{}
	for _, addr := range watched {
		store[addr] = struct{}{}
	}
	return NewTronIndexer("tron", config.ChainConfig{NetworkId: tronMainnetNetworkID}, nil, store)
}

func TestTronProcessBlock_MainnetTRC20DepositHasBlockHashAndTransferIndex(t *testing.T) {
	block, info := loadTronMainnetFixture(t)

	got, err := newTestTronIndexer(tronMainnetTo).processBlock(block, []*tron.TxnInfo{info})
	require.NoError(t, err)

	assert.Equal(t, tronMainnetBlockID, got.Hash)
	require.Len(t, got.Transactions, 1)

	tx := got.Transactions[0]
	assert.Equal(t, tronMainnetTxID, tx.TxHash)
	assert.Equal(t, tronMainnetNetworkID, tx.NetworkId)
	assert.Equal(t, uint64(87831636), tx.BlockNumber)
	assert.Equal(t, tronMainnetBlockID, tx.BlockHash)
	assert.Equal(t, "log:0", tx.TransferIndex)
	assert.Equal(t, tronMainnetFrom, tx.FromAddress)
	assert.Equal(t, tronMainnetTo, tx.ToAddress)
	assert.Equal(t, "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", tx.AssetAddress)
	assert.Equal(t, "15015015", tx.Amount)
	assert.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
}

func TestTronProcessBlock_TRC20TransfersInSameTxGetDistinctHashes(t *testing.T) {
	block, info := loadTronMainnetFixture(t)
	// Same from/to twice in one tx (e.g. a batch payout) used to share one Hash() and be deduped.
	info.Log = append(info.Log, info.Log[0])

	got, err := newTestTronIndexer(tronMainnetTo).processBlock(block, []*tron.TxnInfo{info})
	require.NoError(t, err)
	require.Len(t, got.Transactions, 2)

	assert.Equal(t, "log:0", got.Transactions[0].TransferIndex)
	assert.Equal(t, "log:1", got.Transactions[1].TransferIndex)
	assert.NotEqual(t, got.Transactions[0].Hash(), got.Transactions[1].Hash())
}

func TestTronProcessBlock_UnwatchedAddressKeepsLogIndexPosition(t *testing.T) {
	block, info := loadTronMainnetFixture(t)
	// An unrelated transfer ahead of ours must not shift our index: it is the log position, not the emitted count.
	unrelated := info.Log[0]
	unrelated.Topics = []string{
		info.Log[0].Topics[0],
		info.Log[0].Topics[1],
		"0000000000000000000000001111111111111111111111111111111111111111",
	}
	info.Log = append([]tron.Log{unrelated}, info.Log...)

	got, err := newTestTronIndexer(tronMainnetTo).processBlock(block, []*tron.TxnInfo{info})
	require.NoError(t, err)
	require.Len(t, got.Transactions, 1)
	assert.Equal(t, "log:1", got.Transactions[0].TransferIndex)
}

func TestTronProcessBlock_NativeTRXTransferHasBlockHashAndTransferIndex(t *testing.T) {
	block, _ := loadTronMainnetFixture(t)
	nativeTxID := "1111111111111111111111111111111111111111111111111111111111111111"
	block.Transactions = []tron.Txn{{
		TxID: nativeTxID,
		RawData: tron.TxnRawData{Contract: []tron.Contract{{
			Type: tron.ContractTypeTransfer,
			Parameter: tron.ContractParam{Value: json.RawMessage(`{
				"owner_address": "41ba21fb50b479029a74f1436b55b224fa096674e8",
				"to_address": "41574e52f24239f252275fdb8d64f42d71e0b9a05d",
				"amount": 1000000
			}`)},
		}}},
		Ret: []tron.TxnRet{{ContractRet: "SUCCESS"}},
	}}

	got, err := newTestTronIndexer(tronMainnetTo).processBlock(block, nil)
	require.NoError(t, err)
	require.Len(t, got.Transactions, 1)

	tx := got.Transactions[0]
	assert.Equal(t, nativeTxID, tx.TxHash)
	assert.Equal(t, tronMainnetBlockID, tx.BlockHash)
	assert.Equal(t, "contract:0", tx.TransferIndex)
	assert.Equal(t, tronMainnetTo, tx.ToAddress)
	assert.Equal(t, "1000000", tx.Amount)
	assert.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
}
