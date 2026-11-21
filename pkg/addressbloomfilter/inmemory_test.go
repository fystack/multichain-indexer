package addressbloomfilter

import (
	"context"
	"errors"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"github.com/stretchr/testify/require"
)

type walletAddressRepoStub struct {
	addresses map[enum.NetworkType][]string
	queried   []enum.NetworkType
}

func (r *walletAddressRepoStub) Find(_ context.Context, options repository.FindOptions) ([]*model.WalletAddress, error) {
	addrType := options.Where["type"].(enum.NetworkType)
	r.queried = append(r.queried, addrType)
	stored, ok := r.addresses[addrType]
	if !ok {
		return nil, errors.New("invalid input value for enum address_type")
	}
	if options.Offset > 0 {
		return nil, nil
	}
	wallets := make([]*model.WalletAddress, 0, len(stored))
	for _, address := range stored {
		wallets = append(wallets, &model.WalletAddress{Address: address})
	}
	return wallets, nil
}

func TestAddressBloomFilter_InitializeLoadsOnlyGivenTypes(t *testing.T) {
	repo := &walletAddressRepoStub{addresses: map[enum.NetworkType][]string{enum.NetworkTypeStellar: {"GSTELLAR"}}}
	filter := NewAddressBloomFilter(Config{WalletAddressRepo: repo, ExpectedItems: 100, FalsePositiveRate: 0.01, BatchSize: 10})

	require.NoError(t, filter.Initialize(context.Background(), []enum.NetworkType{enum.NetworkTypeStellar}))

	require.Equal(t, []enum.NetworkType{enum.NetworkTypeStellar, enum.NetworkTypeStellar}, repo.queried)
	require.True(t, filter.Contains("GSTELLAR", enum.NetworkTypeStellar))
}
