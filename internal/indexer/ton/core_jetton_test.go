package ton

import (
	"context"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
)

type mockTonAPI struct {
	resolveMasterFn func(ctx context.Context, jettonWallet string) (string, error)
	resolveCalls    int
}

func (m *mockTonAPI) ListTransactions(_ context.Context, _ *address.Address, _ uint32, _ uint64, _ []byte) ([]*tlb.Transaction, error) {
	return nil, nil
}

func (m *mockTonAPI) GetLatestMasterchainSeqno(_ context.Context) (uint64, error) {
	return 0, nil
}

func (m *mockTonAPI) ResolveJettonMasterAddress(ctx context.Context, jettonWallet string) (string, error) {
	m.resolveCalls++
	if m.resolveMasterFn == nil {
		return "", nil
	}
	return m.resolveMasterFn(ctx, jettonWallet)
}

func TestResolveJettonAssetAddresses_ResolveAndCache(t *testing.T) {
	const (
		walletAddr = "0:eaa27e0e4fbadad817ac4a106de2bae8b52106b5267c1656a3892538d59c69dc"
		masterAddr = "0:ca6e321c3ce184f66f4f74344770f31472800583947a7f9d5f68fecf052ce20f"
	)

	api := &mockTonAPI{
		resolveMasterFn: func(_ context.Context, jettonWallet string) (string, error) {
			if jettonWallet == walletAddr {
				return masterAddr, nil
			}
			return "", nil
		},
	}

	idx := &TonAccountIndexer{
		client:         api,
		jettonRegistry: NewConfigBasedRegistry(nil),
		jettonMasters:  make(map[string]string),
	}

	first := []types.Transaction{
		{
			Type:         constant.TxTypeTokenTransfer,
			AssetAddress: walletAddr,
		},
	}
	idx.resolveJettonAssetAddresses(context.Background(), first)
	assert.Equal(t, masterAddr, first[0].AssetAddress)
	assert.Equal(t, 1, api.resolveCalls)

	second := []types.Transaction{
		{
			Type:         constant.TxTypeTokenTransfer,
			AssetAddress: walletAddr,
		},
	}
	idx.resolveJettonAssetAddresses(context.Background(), second)
	assert.Equal(t, masterAddr, second[0].AssetAddress)
	assert.Equal(t, 1, api.resolveCalls)
}
