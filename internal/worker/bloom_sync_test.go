package worker

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/stretchr/testify/require"
)

type recordingAddressLoader struct {
	queried []enum.NetworkType
}

func (l *recordingAddressLoader) LoadAddresses(_ context.Context, params AddressLoaderParams) ([]LoadedAddress, error) {
	l.queried = append(l.queried, params.NetworkType)
	return nil, nil
}

func TestBloomSyncWorker_SyncsOnlyConfiguredNetworkTypes(t *testing.T) {
	loader := &recordingAddressLoader{}
	worker := NewBloomSyncWorker(context.Background(), nil, loader, BloomSyncConfig{
		BatchSize:    10,
		NetworkTypes: []enum.NetworkType{enum.NetworkTypeStellar},
	})

	worker.initLastSyncedTimes()
	worker.syncAllNetworks()

	require.Equal(t, []enum.NetworkType{enum.NetworkTypeStellar}, loader.queried)
}
