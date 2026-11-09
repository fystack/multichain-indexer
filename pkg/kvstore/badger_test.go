package kvstore

import (
	"testing"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBadgerList_KeysRoundTripThroughGet(t *testing.T) {
	store, err := NewBadgerStore(t.TempDir(), "indexer", infra.JSON)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.Set("block_states/eth/latest", "42"))

	pairs, err := store.List("block_states/")
	require.NoError(t, err)
	require.Len(t, pairs, 1)
	assert.Equal(t, "block_states/eth/latest", pairs[0].Key)

	got, err := store.Get(pairs[0].Key)
	require.NoError(t, err)
	assert.Equal(t, "42", got)
}
