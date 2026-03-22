package indexer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"testing"
	"time"

	tonrpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	tonlib "github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

func TestNormalizeTONAddressRaw(t *testing.T) {
	addr := "0:fc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e"

	got := normalizeTONAddressRaw(
		" 0:FC58A2BB35B051810BEF84FCE18747AC2C2CFCBE0CE3D3167193D9B2538EF33E ",
	)
	assert.Equal(t, addr, got)

	got = normalizeTONAddressRaw("0:0xfc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e")
	assert.Equal(t, addr, got)

	assert.Equal(t, "", normalizeTONAddressRaw("not-a-ton-address"))
}

func TestEncodeTONTxHash(t *testing.T) {
	hash := []byte{0xfb, 0xef, 0xff, 0xfa, 0x00, 0x01, 0x02, 0x7f, 0x80, 0x90, 0xaa}
	encoded := encodeTONTxHash(hash)
	assert.Equal(t, "fbeffffa0001027f8090aa", encoded)

	decoded, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, hash, decoded)
}

func TestBuildTONFlowID_UsesOnlyQueryIDForCorrelatedFlows(t *testing.T) {
	t.Parallel()

	txA := types.Transaction{
		TxHash:       "a",
		FromAddress:  "user",
		ToAddress:    "wallet",
		AssetAddress: "jettonA",
		Type:         constant.TxTypeTokenTransfer,
		Metadata: map[string]any{
			"protocol": "dedust",
			"subtype":  "transfer",
		},
	}
	txB := types.Transaction{
		TxHash:       "b",
		FromAddress:  "vault",
		ToAddress:    "user",
		AssetAddress: "jettonB",
		Type:         constant.TxTypeTokenTransfer,
		Metadata: map[string]any{
			"protocol": "tep74",
			"subtype":  "transfer_notification",
		},
	}

	assert.Equal(t, "q:77", buildTONFlowID(77, txA))
	assert.Equal(t, "q:77", buildTONFlowID(77, txB))
	assert.Equal(t, "tx:a:transfer", buildTONFlowID(0, txA))
}

func TestNewTonIndexerWorkerKnobs(t *testing.T) {
	t.Parallel()

	idx := NewTonIndexer(
		"ton_mainnet",
		config.ChainConfig{
			InternalCode: "TON_MAINNET",
			Throttle: config.Throttle{
				Concurrency: 2,
			},
			Ton: config.TonConfig{
				ShardScanWorkers: 1,
				TxFetchWorkers:   6,
			},
		},
		nil,
		nil,
		nil,
	)

	require.Equal(t, 1, idx.shardScanWorkers)
	require.Equal(t, 6, idx.txFetchConc)
}

func TestNewTonIndexerWorkerKnobsFallbackToThrottle(t *testing.T) {
	t.Parallel()

	idx := NewTonIndexer(
		"ton_mainnet",
		config.ChainConfig{
			InternalCode: "TON_MAINNET",
			Throttle: config.Throttle{
				Concurrency: 3,
			},
		},
		nil,
		nil,
		nil,
	)

	require.Equal(t, 3, idx.shardScanWorkers)
	require.Equal(t, 3, idx.txFetchConc)
}

func TestTonIndexerBackfillsIntermediateShardBlocks(t *testing.T) {
	t.Parallel()

	baseShard := int64(-1 << 63)

	master100 := testMasterBlockID(100)
	master101 := testMasterBlockID(101)
	shard100 := testShardBlockID(0, baseShard, 100)
	shard102 := testShardBlockID(0, baseShard, 102)

	api := &tonAPIStub{
		masterBySeq: map[uint32]*tonlib.BlockIDExt{
			100: master100,
			101: master101,
		},
		shardsByMasterSeq: map[uint32][]*tonlib.BlockIDExt{
			100: {shard100},
			101: {shard102},
		},
		blockDataBySeq: map[uint32]*tlb.Block{
			102: testLinearParentBlock(0, baseShard, 101),
			101: testLinearParentBlock(0, baseShard, 100),
		},
	}

	idx := NewTonIndexer(
		"ton_mainnet",
		config.ChainConfig{
			InternalCode: "TON_MAINNET",
			Throttle: config.Throttle{
				Concurrency: 2,
			},
		},
		api,
		nil,
		nil,
	)

	ctx := context.Background()

	_, err := idx.GetBlock(ctx, 100)
	require.NoError(t, err)

	_, err = idx.GetBlock(ctx, 101)
	require.NoError(t, err)

	assert.Equal(t, []uint32{100, 101, 102}, api.txScanCalls())
	assert.Equal(t, []uint32{102, 101}, api.blockDataCalls())
}

func TestTonIndexerBackfillHandlesSplitViaParentTraversal(t *testing.T) {
	t.Parallel()

	baseShard := int64(-1 << 63)

	leftChild := int64(tlb.ShardChild(uint64(baseShard), true))

	master100 := testMasterBlockID(100)
	master101 := testMasterBlockID(101)
	shard100 := testShardBlockID(0, baseShard, 100)
	left101 := testShardBlockID(0, leftChild, 101)

	api := &tonAPIStub{
		masterBySeq: map[uint32]*tonlib.BlockIDExt{
			100: master100,
			101: master101,
		},
		shardsByMasterSeq: map[uint32][]*tonlib.BlockIDExt{
			100: {shard100},
			101: {left101},
		},
		blockDataBySeq: map[uint32]*tlb.Block{
			101: testAfterSplitParentBlock(0, leftChild, 100),
		},
	}

	idx := NewTonIndexer(
		"ton_mainnet",
		config.ChainConfig{
			InternalCode: "TON_MAINNET",
			Throttle: config.Throttle{
				Concurrency: 2,
			},
		},
		api,
		nil,
		nil,
	)

	ctx := context.Background()

	_, err := idx.GetBlock(ctx, 100)
	require.NoError(t, err)

	_, err = idx.GetBlock(ctx, 101)
	require.NoError(t, err)

	assert.Equal(t, []uint32{100, 101}, api.txScanCalls())
	assert.Equal(t, []uint32{101}, api.blockDataCalls())
}

func TestTonParseJettonTransferWithDeDustForwardPayload(t *testing.T) {
	t.Parallel()

	user := mustTONAddress(t, "0:1111111111111111111111111111111111111111111111111111111111111111")
	recipient := mustTONAddress(t, "0:2222222222222222222222222222222222222222222222222222222222222222")
	jettonWallet := mustTONAddress(t, "0:3333333333333333333333333333333333333333333333333333333333333333")
	master := mustTONAddress(t, "0:4444444444444444444444444444444444444444444444444444444444444444")

	api := &tonAPIStub{
		jettonMasterByWallet: map[string]string{
			jettonWallet.StringRaw(): master.StringRaw(),
		},
	}
	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, api, nil, nil)

	tx := testTONInboundTx(
		user,
		jettonWallet,
		0,
		testJettonTransferBody(77, 500, recipient, user, cell.BeginCell().MustStoreUInt(tonDeDustJettonSwapOpcode, 32).EndCell()),
	)

	got := idx.parseMatchedTransactions(context.Background(), tx)
	require.Len(t, got, 1)
	assert.Equal(t, constant.TxTypeTokenTransfer, got[0].Type)
	assert.Equal(t, "dedust", got[0].GetMetadataString("protocol"))
	assert.Equal(t, "dedust_swap", got[0].GetMetadataString("subtype"))
	assert.Equal(t, recipient.StringRaw(), got[0].ToAddress)
	assert.Equal(t, master.StringRaw(), got[0].AssetAddress)
	assert.Equal(t, "q:77", got[0].GetMetadataString("flow_id"))
}

func TestTonParseJettonExcessesAsNativeRefund(t *testing.T) {
	t.Parallel()

	router := mustTONAddress(t, "0:5555555555555555555555555555555555555555555555555555555555555555")
	user := mustTONAddress(t, "0:6666666666666666666666666666666666666666666666666666666666666666")

	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, &tonAPIStub{}, nil, nil)
	tx := testTONInboundTx(router, user, 12, testOpcodeWithQueryBody(tonJettonExcessesOpcode, 42))

	got := idx.parseMatchedTransactions(context.Background(), tx)
	require.Len(t, got, 1)
	assert.Equal(t, constant.TxTypeNativeTransfer, got[0].Type)
	assert.Equal(t, "excesses", got[0].GetMetadataString("subtype"))
	assert.Equal(t, "q:42", got[0].GetMetadataString("flow_id"))
	assert.Equal(t, "0", got[0].TransferIndex)
}

func TestTonParseJettonBurn(t *testing.T) {
	t.Parallel()

	user := mustTONAddress(t, "0:7777777777777777777777777777777777777777777777777777777777777777")
	jettonWallet := mustTONAddress(t, "0:8888888888888888888888888888888888888888888888888888888888888888")
	master := mustTONAddress(t, "0:9999999999999999999999999999999999999999999999999999999999999999")

	api := &tonAPIStub{
		jettonMasterByWallet: map[string]string{
			jettonWallet.StringRaw(): master.StringRaw(),
		},
		jettonOwnerByWallet: map[string]string{
			jettonWallet.StringRaw(): user.StringRaw(),
		},
	}
	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, api, nil, nil)
	tx := testTONInboundTx(user, jettonWallet, 1, testJettonBurnBody(91, 700, user))

	got := idx.parseMatchedTransactions(context.Background(), tx)
	require.Len(t, got, 1)
	assert.Equal(t, constant.TxTypeTokenTransfer, got[0].Type)
	assert.Equal(t, "burn", got[0].GetMetadataString("subtype"))
	assert.Equal(t, user.StringRaw(), got[0].FromAddress)
	assert.Equal(t, master.StringRaw(), got[0].AssetAddress)
	assert.Equal(t, "0", got[0].TransferIndex)
}

func TestTonGetBlockSetsBlockHashOnTransactions(t *testing.T) {
	t.Parallel()

	api := &tonAPIStub{
		masterBySeq: map[uint32]*tonlib.BlockIDExt{
			100: testMasterBlockID(100),
		},
		shardsByMasterSeq: map[uint32][]*tonlib.BlockIDExt{
			100: {},
		},
	}

	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, api, nil, nil)
	block, err := idx.GetBlock(context.Background(), 100)
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, masterBlockHash(testMasterBlockID(100)), block.Hash)
	for _, tx := range block.Transactions {
		assert.Equal(t, block.Hash, tx.BlockHash)
	}
}

func TestTonCorrelateJettonTransferFlow(t *testing.T) {
	t.Parallel()

	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, &tonAPIStub{}, nil, nil)
	flowID := "q:100|token_transfer|jetton|sender|receiver|"
	group := []types.Transaction{
		{TxHash: "a", Type: constant.TxTypeTokenTransfer, FromAddress: "sender", ToAddress: "receiver", AssetAddress: "jetton", Amount: "100", Metadata: map[string]any{"subtype": "transfer", "flow_id": flowID}},
		{TxHash: "b", Type: constant.TxTypeTokenTransfer, FromAddress: "sender", ToAddress: "receiver", AssetAddress: "jetton", Amount: "100", Metadata: map[string]any{"subtype": "internal_transfer", "flow_id": flowID}},
		{TxHash: "c", Type: constant.TxTypeTokenTransfer, FromAddress: "sender", ToAddress: "receiver", AssetAddress: "jetton", Amount: "100", Metadata: map[string]any{"subtype": "transfer_notification", "flow_id": flowID}},
	}

	got := idx.correlateTONTransactions(group)
	require.Len(t, got, 1)
	assert.Equal(t, "transfer", got[0].GetMetadataString("subtype"))
}

func TestTonCorrelateTokenRefundFlow(t *testing.T) {
	t.Parallel()

	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, &tonAPIStub{}, nil, nil)
	flowID := "q:101|token_transfer|jetton|user|router|"
	group := []types.Transaction{
		{TxHash: "a", Type: constant.TxTypeTokenTransfer, FromAddress: "user", ToAddress: "router", AssetAddress: "jetton", Amount: "100", Metadata: map[string]any{"subtype": "transfer", "flow_id": flowID}},
		{TxHash: "b", Type: constant.TxTypeTokenTransfer, FromAddress: "router", ToAddress: "user", AssetAddress: "jetton", Amount: "100", Metadata: map[string]any{"subtype": "transfer_notification", "flow_id": flowID}},
	}

	got := idx.correlateTONTransactions(group)
	require.Len(t, got, 1)
	assert.Equal(t, constant.TxTypeTokenTransfer, got[0].Type)
	assert.Equal(t, "refund", got[0].GetMetadataString("subtype"))
}

func TestTonCorrelateSwapFlow(t *testing.T) {
	t.Parallel()

	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, &tonAPIStub{}, nil, nil)
	flowID := "q:102|token_transfer|jettonA|user|vault|dedust"
	group := []types.Transaction{
		{TxHash: "a", Type: constant.TxTypeTokenTransfer, FromAddress: "user", ToAddress: "vault", AssetAddress: "jettonA", Amount: "100", Metadata: map[string]any{"subtype": "dedust_swap", "protocol": "dedust", "flow_id": flowID}},
		{TxHash: "b", Type: constant.TxTypeTokenTransfer, FromAddress: "vault", ToAddress: "user", AssetAddress: "jettonB", Amount: "250", Metadata: map[string]any{"subtype": "transfer_notification", "protocol": "tep74", "flow_id": flowID}},
	}

	got := idx.correlateTONTransactions(group)
	require.Len(t, got, 2)
	assert.Equal(t, constant.TxTypeTokenTransfer, got[0].Type)
	assert.Equal(t, constant.TxTypeTokenTransfer, got[1].Type)
	assert.Equal(t, "dedust", got[1].GetMetadataString("protocol"))
}

func TestTonMainnetFetchAndParseTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	type tonRealTxCase struct {
		name         string
		masterSeq    uint32
		workchain    int32
		shard        int64
		blockSeq     uint32
		account      string
		lt           uint64
		wantType     constant.TxType
		wantSubtype  string
		wantProtocol string
	}

	// Fill these with real TON mainnet transactions.
	// TON lite client fetches a tx by exact account block + account address + logical time,
	// not by tx hash alone. In practice you can get these values from tonviewer/tonscan:
	// - masterSeq: masterchain seqno near the tx block
	// - workchain/shard/blockSeq: exact shard block containing the account tx
	// - account: raw TON address of the account that owns the transaction
	// - lt: logical time of the transaction for that account
	testCases := []tonRealTxCase{
		{
			name:      "native transfer",
			workchain: 0,
			shard:     int64(-1 << 63),
			blockSeq:  63190763,
			account:   "UQBZs1e2h5CwmxQxmAJLGNqEPcQ9iU3BCDj0NSzbwTiGa3hR",
			lt:        68167866000003,
			wantType:  constant.TxTypeNativeTransfer,
		},
		{
			name:      "jetton transfer",
			workchain: 0,
			shard:     int64(-1 << 63),
			blockSeq:  63154602,
			account:   "EQDIFXJVZiKgxkrjrkHJx3rmq_jfanMKc8Avs8QG1uor2prW",
			lt:        68128875000003,
			wantType:  constant.TxTypeTokenTransfer,
		},
		{
			name:        "jetton excesses",
			workchain:   0,
			shard:       int64(-1 << 63),
			blockSeq:    63154602,
			account:     "UQC59MDUNL4DPOIepE84eL7jZ3GZw0euF3HaojDjMvnJey7u",
			lt:          68128875000008,
			wantType:    constant.TxTypeNativeTransfer,
			wantSubtype: "excesses",
		},
		{
			name:        "jetton burn",
			workchain:   0,
			shard:       int64(-1 << 63),
			blockSeq:    63190882,
			account:     "EQBUuLy0JXTEdnXqfQlDD81chthHlqE1FvR6DGSHMY7tg1S0",
			lt:          68167993000003,
			wantType:    constant.TxTypeTokenTransfer,
			wantSubtype: "burn",
		},
		{
			name:         "dedust or stonfi swap",
			workchain:    0,
			shard:        int64(-1 << 63),
			blockSeq:     63191560,
			account:      "EQB3ncyBUTjZUA5EnFKR5_EnOMI9V1tTEAAPaiU71gc4TiUt",
			lt:           68168718000007,
			wantType:     constant.TxTypeTokenTransfer,
			wantSubtype:  "transfer_notification",
			wantProtocol: "tep74",
		},
	}

	enabled := false
	for _, tc := range testCases {
		if tc.account != "" && tc.lt != 0 && tc.blockSeq != 0 {
			enabled = true
			break
		}
	}
	if !enabled {
		t.Skip("real TON mainnet cases are empty; fill account/lt/block identifiers first")
	}

	client, err := tonrpc.NewClient(context.Background(), tonrpc.ClientConfig{
		ConfigURL: "https://ton.org/global.config.json",
	})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	idx := NewTonIndexer("ton_mainnet", config.ChainConfig{InternalCode: "TON_MAINNET"}, client, nil, nil)

	for _, tc := range testCases {
		tc := tc
		if tc.account == "" || tc.lt == 0 || tc.blockSeq == 0 {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			ctx = client.StickyContext(ctx)

			addr, err := parseTONAddress(tc.account)
			require.NoError(t, err, "account must be valid TON address")

			master, err := client.GetLatestMasterchainInfo(ctx)
			require.NoError(t, err, "should resolve latest masterchain info")

			accountState, err := client.GetAccount(ctx, master, addr)
			require.NoError(t, err, "should load account state")
			require.NotNil(t, accountState)

			tx, err := findTONTransactionByLT(ctx, client, addr, tc.lt, accountState.LastTxLT, accountState.LastTxHash)
			require.NoError(t, err, "should fetch real TON tx from account history")
			require.NotNil(t, tx)

			parsed := idx.parseMatchedTransactions(ctx, tx)
			require.NotEmpty(t, parsed, "should classify at least one semantic movement from mainnet tx")

			found := false
			for _, item := range parsed {
				require.Contains(t, []constant.TxType{constant.TxTypeNativeTransfer, constant.TxTypeTokenTransfer}, item.Type)
				require.NotEmpty(t, item.Type)
				require.NotEmpty(t, item.TxHash)
				require.NotEmpty(t, item.ToAddress)
				if item.Type == tc.wantType {
					if tc.wantSubtype != "" && item.GetMetadataString("subtype") != tc.wantSubtype {
						continue
					}
					if tc.wantProtocol != "" && item.GetMetadataString("protocol") != tc.wantProtocol {
						continue
					}
					found = true
				}
			}
			require.True(t, found, "expected tx type %s in parsed outputs", tc.wantType)
		})
	}
}

type tonAPIStub struct {
	mu sync.Mutex

	masterBySeq          map[uint32]*tonlib.BlockIDExt
	shardsByMasterSeq    map[uint32][]*tonlib.BlockIDExt
	blockDataBySeq       map[uint32]*tlb.Block
	jettonWalletByPair   map[string]string
	jettonOwnerByWallet  map[string]string
	jettonMasterByWallet map[string]string

	txSeqnos   []uint32
	dataSeqnos []uint32
}

var _ tonrpc.TonAPI = (*tonAPIStub)(nil)

func (s *tonAPIStub) StickyContext(ctx context.Context) context.Context { return ctx }

func (s *tonAPIStub) GetLatestMasterchainInfo(context.Context) (*tonlib.BlockIDExt, error) {
	return nil, fmt.Errorf("not implemented in test")
}

func (s *tonAPIStub) LookupMasterchainBlock(_ context.Context, seqno uint32) (*tonlib.BlockIDExt, error) {
	master, ok := s.masterBySeq[seqno]
	if !ok {
		return nil, fmt.Errorf("master block not found: %d", seqno)
	}
	return master, nil
}

func (s *tonAPIStub) LookupBlock(
	_ context.Context,
	workchain int32,
	shard int64,
	seqno uint32,
) (*tonlib.BlockIDExt, error) {
	return testShardBlockID(workchain, shard, seqno), nil
}

func (s *tonAPIStub) GetBlockData(_ context.Context, block *tonlib.BlockIDExt) (*tlb.Block, error) {
	s.mu.Lock()
	s.dataSeqnos = append(s.dataSeqnos, block.SeqNo)
	s.mu.Unlock()

	data, ok := s.blockDataBySeq[block.SeqNo]
	if !ok {
		return nil, fmt.Errorf("block data not found: %d", block.SeqNo)
	}
	return data, nil
}

func (s *tonAPIStub) GetBlockShardsInfo(
	_ context.Context,
	master *tonlib.BlockIDExt,
) ([]*tonlib.BlockIDExt, error) {
	shards, ok := s.shardsByMasterSeq[master.SeqNo]
	if !ok {
		return nil, fmt.Errorf("shards not found for master: %d", master.SeqNo)
	}
	return shards, nil
}

func (s *tonAPIStub) GetBlockTransactionsV2(
	_ context.Context,
	_ uint32,
	block *tonlib.BlockIDExt,
	_ uint32,
	_ *tonlib.TransactionID3,
) ([]tonlib.TransactionShortInfo, bool, error) {
	s.mu.Lock()
	s.txSeqnos = append(s.txSeqnos, block.SeqNo)
	s.mu.Unlock()
	return nil, false, nil
}

func (s *tonAPIStub) GetTransaction(
	context.Context,
	*tonlib.BlockIDExt,
	*address.Address,
	uint64,
) (*tlb.Transaction, error) {
	return nil, fmt.Errorf("not implemented in test")
}

func (s *tonAPIStub) GetAccount(context.Context, *tonlib.BlockIDExt, *address.Address) (*tlb.Account, error) {
	return nil, fmt.Errorf("not implemented in test")
}

func (s *tonAPIStub) ListTransactions(context.Context, *address.Address, uint32, uint64, []byte) ([]*tlb.Transaction, error) {
	return nil, fmt.Errorf("not implemented in test")
}

func (s *tonAPIStub) ResolveJettonWalletAddress(
	_ context.Context,
	master string,
	owner string,
) (string, error) {
	if s.jettonWalletByPair == nil {
		return "", fmt.Errorf("not implemented in test")
	}
	wallet := s.jettonWalletByPair[master+"|"+owner]
	if wallet == "" {
		return "", fmt.Errorf("wallet pair not found")
	}
	return wallet, nil
}

func (s *tonAPIStub) ResolveJettonWalletData(_ context.Context, wallet string) (string, string, error) {
	owner := ""
	master := ""
	if s.jettonOwnerByWallet != nil {
		owner = s.jettonOwnerByWallet[wallet]
	}
	if s.jettonMasterByWallet != nil {
		master = s.jettonMasterByWallet[wallet]
	}
	if owner == "" && master == "" {
		return "", "", fmt.Errorf("wallet data not found")
	}
	return owner, master, nil
}

func (s *tonAPIStub) ResolveJettonMasterAddress(_ context.Context, wallet string) (string, error) {
	if s.jettonMasterByWallet == nil {
		return "", fmt.Errorf("not implemented in test")
	}
	master := s.jettonMasterByWallet[wallet]
	if master == "" {
		return "", fmt.Errorf("master not found")
	}
	return master, nil
}

func (s *tonAPIStub) Close() error { return nil }

func (s *tonAPIStub) txScanCalls() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]uint32, len(s.txSeqnos))
	copy(out, s.txSeqnos)
	return out
}

func (s *tonAPIStub) blockDataCalls() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]uint32, len(s.dataSeqnos))
	copy(out, s.dataSeqnos)
	return out
}

func testMasterBlockID(seqno uint32) *tonlib.BlockIDExt {
	return &tonlib.BlockIDExt{
		Workchain: -1,
		Shard:     -1 << 63,
		SeqNo:     seqno,
		RootHash:  []byte{byte(seqno)},
		FileHash:  []byte{byte(seqno + 1)},
	}
}

func testShardBlockID(workchain int32, shard int64, seqno uint32) *tonlib.BlockIDExt {
	return &tonlib.BlockIDExt{
		Workchain: workchain,
		Shard:     shard,
		SeqNo:     seqno,
		RootHash:  []byte{byte(seqno)},
		FileHash:  []byte{byte(seqno + 1)},
	}
}

func testLinearParentBlock(workchain int32, shard int64, parentSeq uint32) *tlb.Block {
	header := tlb.BlockHeader{}
	header.AfterMerge = false
	header.AfterSplit = false
	header.Shard = testShardIdent(workchain, shard)
	header.PrevRef = tlb.BlkPrevInfo{
		Prev1: tlb.ExtBlkRef{
			SeqNo:    parentSeq,
			RootHash: []byte{byte(parentSeq)},
			FileHash: []byte{byte(parentSeq + 1)},
		},
	}

	return &tlb.Block{
		BlockInfo: header,
	}
}

func testAfterSplitParentBlock(workchain int32, shard int64, parentSeq uint32) *tlb.Block {
	header := tlb.BlockHeader{}
	header.AfterMerge = false
	header.AfterSplit = true
	header.Shard = testShardIdent(workchain, shard)
	header.PrevRef = tlb.BlkPrevInfo{
		Prev1: tlb.ExtBlkRef{
			SeqNo:    parentSeq,
			RootHash: []byte{byte(parentSeq)},
			FileHash: []byte{byte(parentSeq + 1)},
		},
	}

	return &tlb.Block{
		BlockInfo: header,
	}
}

func testShardIdent(workchain int32, shard int64) tlb.ShardIdent {
	u := uint64(shard)
	marker := u & (^u + 1)
	if marker == 0 {
		marker = uint64(1) << 63
	}
	prefixBits := int8(63 - bits.TrailingZeros64(marker))

	return tlb.ShardIdent{
		PrefixBits:  prefixBits,
		WorkchainID: workchain,
		ShardPrefix: u &^ marker,
	}
}

func findTONTransactionByLT(
	ctx context.Context,
	client tonrpc.TonAPI,
	addr *address.Address,
	targetLT uint64,
	lastLT uint64,
	lastHash []byte,
) (*tlb.Transaction, error) {
	for {
		if lastLT == 0 || len(lastHash) == 0 {
			return nil, fmt.Errorf("transaction lt=%d not found in account history", targetLT)
		}

		list, err := client.ListTransactions(ctx, addr, 15, lastLT, lastHash)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("transaction lt=%d not found in account history", targetLT)
		}

		for _, tx := range list {
			if tx != nil && tx.LT == targetLT {
				return tx, nil
			}
		}

		oldest := list[0]
		if oldest == nil {
			return nil, errors.New("received nil transaction from account history")
		}
		lastLT = oldest.PrevTxLT
		lastHash = oldest.PrevTxHash
	}
}

type tonPubkeyStoreStub map[string]struct{}

func (s tonPubkeyStoreStub) Exist(addressType enum.NetworkType, address string) bool {
	if addressType != enum.NetworkTypeTon {
		return false
	}
	_, ok := s[address]
	return ok
}

func mustTONAddress(t *testing.T, raw string) *address.Address {
	t.Helper()
	addr, err := address.ParseRawAddr(raw)
	require.NoError(t, err)
	return addr
}

func testTONInboundTx(src, dst *address.Address, amountTON uint64, body *cell.Cell) *tlb.Transaction {
	return &tlb.Transaction{
		Now:  1710000000,
		Hash: []byte{0xaa, 0xbb, 0xcc},
		IO: struct {
			In  *tlb.Message      `tlb:"maybe ^"`
			Out *tlb.MessagesList `tlb:"maybe ^"`
		}{
			In: &tlb.Message{
				MsgType: tlb.MsgTypeInternal,
				Msg: &tlb.InternalMessage{
					SrcAddr: src,
					DstAddr: dst,
					Amount:  tlb.MustFromTON(fmt.Sprintf("%d", amountTON)),
					Body:    body,
				},
			},
		},
		TotalFees: tlb.CurrencyCollection{
			Coins: tlb.MustFromTON("0.01"),
		},
	}
}

func testJettonTransferBody(queryID, amount uint64, destination, responseDestination *address.Address, forwardPayload *cell.Cell) *cell.Cell {
	builder := cell.BeginCell().
		MustStoreUInt(tonJettonTransferOpcode, 32).
		MustStoreUInt(queryID, 64).
		MustStoreVarUInt(amount, 16).
		MustStoreAddr(destination).
		MustStoreAddr(responseDestination).
		MustStoreMaybeRef(nil).
		MustStoreVarUInt(1, 16)

	if forwardPayload != nil {
		builder.MustStoreBoolBit(true).MustStoreRef(forwardPayload)
	} else {
		builder.MustStoreBoolBit(false)
	}
	return builder.EndCell()
}

func testOpcodeWithQueryBody(opcode, queryID uint64) *cell.Cell {
	return cell.BeginCell().
		MustStoreUInt(opcode, 32).
		MustStoreUInt(queryID, 64).
		EndCell()
}

func testJettonBurnBody(queryID, amount uint64, responseDestination *address.Address) *cell.Cell {
	return cell.BeginCell().
		MustStoreUInt(tonJettonBurnOpcode, 32).
		MustStoreUInt(queryID, 64).
		MustStoreVarUInt(amount, 16).
		MustStoreAddr(responseDestination).
		MustStoreMaybeRef(nil).
		EndCell()
}
