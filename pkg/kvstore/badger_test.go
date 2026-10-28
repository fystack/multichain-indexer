package kvstore

import (
	"strings"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The store keeps working while its GC goroutine runs, and Close stops that goroutine.
func TestBadgerStore_RewritesAndCloseWithGC(t *testing.T) {
	dir := t.TempDir()
	store, err := NewBadgerStore(dir, "test", infra.JSON)
	require.NoError(t, err)

	big := strings.Repeat("x", 2<<20) // above the value threshold, so it lands in the value log
	for i := 0; i < 50; i++ {
		require.NoError(t, store.Set("hot", big+string(rune('a'+i%26))))
	}
	value, err := store.Get("hot")
	require.NoError(t, err)
	assert.Equal(t, big+string(rune('a'+49%26)), value)
	require.NoError(t, store.Close())

	reopened, err := NewBadgerStore(dir, "test", infra.JSON)
	require.NoError(t, err)
	value, err = reopened.Get("hot")
	require.NoError(t, err)
	assert.Equal(t, big+string(rune('a'+49%26)), value)
	require.NoError(t, reopened.Close())
}
