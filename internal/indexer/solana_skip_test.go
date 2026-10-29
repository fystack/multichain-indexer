package indexer

import (
	"context"
	"errors"
	"testing"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/adaptive"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSolanaAPI struct {
	solana.SolanaAPI
	blocks    map[uint64]*solana.GetBlockResult
	produced  []uint64
	blocksErr error
}

func (f *fakeSolanaAPI) GetBlock(_ context.Context, slot uint64) (*solana.GetBlockResult, error) {
	return f.blocks[slot], nil
}

func (f *fakeSolanaAPI) GetBlocks(_ context.Context, start, end uint64) ([]uint64, error) {
	if f.blocksErr != nil {
		return nil, f.blocksErr
	}
	var out []uint64
	for _, s := range f.produced {
		if s >= start && s <= end {
			out = append(out, s)
		}
	}
	return out, nil
}

func newSkipTestIndexer(t *testing.T, api *fakeSolanaAPI) *SolanaIndexer {
	f := rpc.NewFailover[solana.SolanaAPI](nil)
	require.NoError(t, f.AddProvider(&rpc.Provider{Name: "fake", Client: api, State: rpc.StateHealthy}))
	return &SolanaIndexer{chainName: "solana", failover: f, limiter: adaptive.New(adaptive.Config{Max: 2})}
}

func TestSolanaSlotConfirmedSkipped(t *testing.T) {
	produced := []uint64{10, 11, 13, 20}
	assert.True(t, solanaSlotConfirmedSkipped(produced, 12))
	assert.False(t, solanaSlotConfirmedSkipped(produced, 13), "produced slot")
	assert.False(t, solanaSlotConfirmedSkipped(produced, 9), "no produced block below")
	assert.False(t, solanaSlotConfirmedSkipped(produced, 21), "no produced block above")
	assert.False(t, solanaSlotConfirmedSkipped(nil, 12))
}

func TestSolanaConfirmSkippedSlots(t *testing.T) {
	block := &solana.GetBlockResult{Blockhash: "h"}
	api := &fakeSolanaAPI{
		// 101 is a real skip; 102 was produced but this node returned null for it; 103 is past the node's tip.
		blocks:   map[uint64]*solana.GetBlockResult{100: block},
		produced: []uint64{99, 100, 102},
	}
	idx := newSkipTestIndexer(t, api)

	results, err := idx.GetBlocksByNumbers(context.Background(), []uint64{100, 101, 102, 103})
	require.NoError(t, err)

	assert.Nil(t, results[0].Error)
	require.NotNil(t, results[1].Error)
	assert.Equal(t, ErrorTypeBlockNotFound, results[1].Error.ErrorType, "confirmed skip")
	assert.Equal(t, ErrorTypeUnknown, results[2].Error.ErrorType, "produced block must be retried")
	assert.Equal(t, ErrorTypeUnknown, results[3].Error.ErrorType, "slot past the node's tip is unconfirmed")
}

func TestSolanaConfirmSkippedSlotsGetBlocksFails(t *testing.T) {
	api := &fakeSolanaAPI{blocksErr: errors.New("boom")}
	idx := newSkipTestIndexer(t, api)

	results := []BlockResult{{Number: 5, Error: &Error{ErrorType: ErrorTypeBlockNotFound}}}
	idx.confirmSkippedSlots(context.Background(), results)

	assert.Equal(t, ErrorTypeUnknown, results[0].Error.ErrorType)
}
