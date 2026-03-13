package indexer

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/bits"
	"sync"
	"testing"

	tonrpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	tonlib "github.com/xssnick/tonutils-go/ton"
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

type tonAPIStub struct {
	mu sync.Mutex

	masterBySeq       map[uint32]*tonlib.BlockIDExt
	shardsByMasterSeq map[uint32][]*tonlib.BlockIDExt
	blockDataBySeq    map[uint32]*tlb.Block

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

func (s *tonAPIStub) ResolveJettonWalletAddress(
	context.Context,
	string,
	string,
) (string, error) {
	return "", fmt.Errorf("not implemented in test")
}

func (s *tonAPIStub) ResolveJettonWalletData(context.Context, string) (string, string, error) {
	return "", "", fmt.Errorf("not implemented in test")
}

func (s *tonAPIStub) ResolveJettonMasterAddress(context.Context, string) (string, error) {
	return "", fmt.Errorf("not implemented in test")
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
