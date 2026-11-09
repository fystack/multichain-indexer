package blockstore

import (
	"errors"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/kvstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKVStore is a minimal infra.KVStore whose Get behavior is configurable.
type fakeKVStore struct {
	getVal      string
	getErr      error
	setAnyCalls int
}

func (f *fakeKVStore) GetName() string                  { return "fake" }
func (f *fakeKVStore) Set(string, string) error         { return nil }
func (f *fakeKVStore) Get(string) (string, error)       { return f.getVal, f.getErr }
func (f *fakeKVStore) SetAny(string, any) error         { f.setAnyCalls++; return nil }
func (f *fakeKVStore) GetAny(string, any) (bool, error) { return false, f.getErr }
func (f *fakeKVStore) List(string) ([]*infra.KVPair, error) {
	return nil, nil
}
func (f *fakeKVStore) Delete(string) error           { return nil }
func (f *fakeKVStore) BatchSet([]infra.KVPair) error { return nil }
func (f *fakeKVStore) Close() error                  { return nil }

func TestGetLatestBlock_MissingKeyReturnsZeroNoError(t *testing.T) {
	bs := NewBlockStore(&fakeKVStore{getErr: kvstore.ErrKeyNotFound})
	latest, err := bs.GetLatestBlock("ETH")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), latest)
}

func TestGetLatestBlock_StoreErrorPropagates(t *testing.T) {
	storeErr := errors.New("redis down")
	bs := NewBlockStore(&fakeKVStore{getErr: storeErr})
	_, err := bs.GetLatestBlock("ETH")
	require.ErrorIs(t, err, storeErr)
}

func TestGetLatestBlock_ParsesValue(t *testing.T) {
	bs := NewBlockStore(&fakeKVStore{getVal: "12345"})
	latest, err := bs.GetLatestBlock("ETH")
	require.NoError(t, err)
	assert.Equal(t, uint64(12345), latest)
}

func TestSaveFailedBlock_ReadErrorDoesNotOverwrite(t *testing.T) {
	storeErr := errors.New("redis timeout")
	kv := &fakeKVStore{getErr: storeErr}
	bs := NewBlockStore(kv)

	require.ErrorIs(t, bs.SaveFailedBlock("ETH", 10), storeErr)
	require.ErrorIs(t, bs.SaveFailedBlocks("ETH", []uint64{10, 11}), storeErr)
	assert.Zero(t, kv.setAnyCalls)
}

func TestSaveFailedBlock_MissingKeyStartsNewList(t *testing.T) {
	kv := &fakeKVStore{getErr: kvstore.ErrKeyNotFound}
	bs := NewBlockStore(kv)

	require.NoError(t, bs.SaveFailedBlock("ETH", 10))
	assert.Equal(t, 1, kv.setAnyCalls)
}
