package types

import (
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestTransactionBinaryRoundTripPreservesRoutingFields(t *testing.T) {
	t.Parallel()

	tx := Transaction{
		TxHash:         "tx-hash",
		NetworkId:      "xrp_mainnet",
		BlockNumber:    42,
		FromAddress:    "rFrom",
		ToAddress:      "rTo",
		AssetAddress:   "",
		DestinationTag: "12345",
		Memo:           "invoice-42",
		MemoType:       "text",
		Amount:         "10.5",
		Type:           constant.TxTypeNativeTransfer,
		TxFee:          decimal.RequireFromString("0.000012"),
		Timestamp:      1234567890,
		Confirmations:  1,
		Status:         StatusConfirmed,
		Direction:      DirectionIn,
	}

	data, err := tx.MarshalBinary()
	require.NoError(t, err)

	var decoded Transaction
	require.NoError(t, decoded.UnmarshalBinary(data))
	require.Equal(t, tx.DestinationTag, decoded.DestinationTag)
	require.Equal(t, tx.Memo, decoded.Memo)
	require.Equal(t, tx.MemoType, decoded.MemoType)
}

func TestTransactionHashIncludesRoutingFields(t *testing.T) {
	t.Parallel()

	base := Transaction{
		TxHash:      "tx-hash",
		NetworkId:   "xrp_mainnet",
		FromAddress: "rFrom",
		ToAddress:   "rTo",
		Timestamp:   1234567890,
		Direction:   DirectionIn,
	}

	withTag := base
	withTag.DestinationTag = "1"

	withMemo := base
	withMemo.Memo = "memo"
	withMemo.MemoType = "text"

	require.NotEqual(t, base.Hash(), withTag.Hash())
	require.NotEqual(t, base.Hash(), withMemo.Hash())
}
