package indexer

import (
	"testing"

	v2 "github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/stretchr/testify/require"
)

func strPtr(v string) *string { return &v }

func TestIsSuiNativeCoinType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		coinType string
		want     bool
	}{
		{
			name:     "canonical short address",
			coinType: "0x2::sui::SUI",
			want:     true,
		},
		{
			name:     "canonical full address",
			coinType: "0x0000000000000000000000000000000000000000000000000000000000000002::sui::SUI",
			want:     true,
		},
		{
			name:     "lowercase struct name",
			coinType: "0x2::sui::sui",
			want:     true,
		},
		{
			name:     "wrapped coin object",
			coinType: "0x2::coin::Coin<0x2::sui::SUI>",
			want:     true,
		},
		{
			name:     "non-native coin",
			coinType: "0x2::usdc::USDC",
			want:     false,
		},
		{
			name:     "empty",
			coinType: "",
			want:     false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isSuiNativeCoinType(tc.coinType))
		})
	}
}

func TestConvertTransactionClassifiesNativeSuiTransfer(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_testnet"},
	}

	from := "0xbd97a67763c8101771308f5a91311c2d826189cc471332d8a6cc5001c00946ee"
	to := "0x476268833af5d2280a1f31bc4a2787cbb37b71f89cb9cbee6662c44fa3091838"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("GMaQqzDioYJHQFseziR6xvWvAvuKaoPUWz3hCrXKhBYs"),
		Transaction: &v2.Transaction{
			Sender: &from,
		},
		BalanceChanges: []*v2.BalanceChange{
			{
				Address:  &to,
				CoinType: strPtr("0x0000000000000000000000000000000000000000000000000000000000000002::sui::SUI"),
				Amount:   strPtr("125000000"),
			},
			{
				Address:  &from,
				CoinType: strPtr("0x2::sui::SUI"),
				Amount:   strPtr("-126997880"),
			},
		},
	}

	tx := s.convertTransaction(execTx, 304566255, 1772704618)
	require.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
	require.Equal(t, "125000000", tx.Amount)
	require.Equal(t, "0x2::sui::SUI", tx.AssetAddress)
	require.Equal(t, to, tx.ToAddress)
}

func TestConvertTransactionClassifiesTokenTransfer(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_testnet"},
	}

	from := "0xsender"
	to := "0xreceiver"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("digest"),
		Transaction: &v2.Transaction{
			Sender: &from,
		},
		BalanceChanges: []*v2.BalanceChange{
			{
				Address:  &to,
				CoinType: strPtr("0x2::usdc::USDC"),
				Amount:   strPtr("500"),
			},
		},
	}

	tx := s.convertTransaction(execTx, 1, 1)
	require.Equal(t, constant.TxTypeTokenTransfer, tx.Type)
	require.Equal(t, "500", tx.Amount)
	require.Equal(t, "0x2::usdc::USDC", tx.AssetAddress)
}
