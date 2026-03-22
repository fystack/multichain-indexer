package indexer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/sui"
	v2 "github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func strPtr(v string) *string                                      { return &v }
func txKindPtr(v v2.TransactionKind_Kind) *v2.TransactionKind_Kind { return &v }

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

func TestConvertTransactionsNormalizesPhaseEventsIntoMovements(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sender := "0x111"
	validator := "0x333"
	recipient := "0x222"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("phase-digest"),
		Transaction: &v2.Transaction{
			Sender: &sender,
			Kind: &v2.TransactionKind{
				Kind: txKindPtr(v2.TransactionKind_PROGRAMMABLE_TRANSACTION),
				Data: &v2.TransactionKind_ProgrammableTransaction{
					ProgrammableTransaction: &v2.ProgrammableTransaction{
						Commands: []*v2.Command{
							{Command: &v2.Command_MoveCall{MoveCall: &v2.MoveCall{
								Package:  strPtr("0x3"),
								Module:   strPtr("sui_system"),
								Function: strPtr("request_add_stake"),
							}}},
							{Command: &v2.Command_MoveCall{MoveCall: &v2.MoveCall{
								Package:  strPtr("0x3"),
								Module:   strPtr("sui_system"),
								Function: strPtr("request_withdraw_stake"),
							}}},
							{Command: &v2.Command_MoveCall{MoveCall: &v2.MoveCall{
								Package:  strPtr("0xswap"),
								Module:   strPtr("pool"),
								Function: strPtr("swap_exact_in"),
							}}},
						},
					},
				},
			},
		},
		BalanceChanges: []*v2.BalanceChange{
			{Address: &sender, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("-1000")},
			{Address: &recipient, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("600")},
			{Address: &sender, CoinType: strPtr("0x2::usdc::USDC"), Amount: strPtr("-50")},
			{Address: &sender, CoinType: strPtr("0x2::weth::WETH"), Amount: strPtr("49")},
			{Address: &validator, CoinType: strPtr("0x2::stake::STAKED_SUI"), Amount: strPtr("700")},
		},
	}

	txs := s.convertTransactions(execTx, 100, 200)
	require.NotEmpty(t, txs)

	typesSeen := map[constant.TxType]typesFixture{}
	for _, tx := range txs {
		require.Contains(t, []constant.TxType{constant.TxTypeNativeTransfer, constant.TxTypeTokenTransfer}, tx.Type)
		typesSeen[tx.Type] = typesFixture{
			To:     tx.ToAddress,
			Asset:  tx.AssetAddress,
			Amount: tx.Amount,
		}
	}

	require.Contains(t, typesSeen, constant.TxTypeNativeTransfer)
	require.Contains(t, typesSeen, constant.TxTypeTokenTransfer)
}

func TestConvertTransactionsEmitsAllPositiveBalanceChanges(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sender := "0xsender"
	r1 := "0xreceiver1"
	r2 := "0xreceiver2"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("multi-transfer"),
		Transaction: &v2.Transaction{
			Sender: &sender,
		},
		BalanceChanges: []*v2.BalanceChange{
			{Address: &sender, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("-300")},
			{Address: &r1, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("100")},
			{Address: &r2, CoinType: strPtr("0x2::usdc::USDC"), Amount: strPtr("200")},
		},
	}

	txs := s.convertTransactions(execTx, 1, 1)
	require.Len(t, txs, 2)

	got := map[string]constant.TxType{}
	for _, tx := range txs {
		got[tx.ToAddress] = tx.Type
	}

	require.Equal(t, constant.TxTypeNativeTransfer, got[r1])
	require.Equal(t, constant.TxTypeTokenTransfer, got[r2])
}

func TestConvertTransactionsEmitsSameTokenMultiSend(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sender := "0xsender"
	r1 := "0xreceiver1"
	r2 := "0xreceiver2"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("same-token-multi-send"),
		Transaction: &v2.Transaction{
			Sender: &sender,
		},
		BalanceChanges: []*v2.BalanceChange{
			{Address: &sender, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("-1000")},
			{Address: &r1, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("600")},
			{Address: &r2, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("400")},
		},
	}

	txs := s.convertTransactions(execTx, 1, 1)
	require.Len(t, txs, 2)

	got := map[string]string{}
	for _, tx := range txs {
		got[tx.ToAddress] = tx.Amount
		require.Equal(t, constant.TxTypeNativeTransfer, tx.Type)
		require.Equal(t, "0x2::sui::SUI", tx.AssetAddress)
		require.Equal(t, sender, tx.FromAddress)
		require.NotEmpty(t, tx.TransferIndex)
	}

	require.Equal(t, "600", got[r1])
	require.Equal(t, "400", got[r2])
}

func TestConvertTransactionsInfersSenderFromBalanceChangesForSponsoredTransfer(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sponsor := "0xsponsor"
	actualSender := "0xactualsender"
	receiver := "0xreceiver"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("sponsored-transfer"),
		Transaction: &v2.Transaction{
			Sender: &sponsor,
		},
		BalanceChanges: []*v2.BalanceChange{
			{Address: &sponsor, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("-10")},
			{Address: &actualSender, CoinType: strPtr("0x2::usdc::USDC"), Amount: strPtr("-500")},
			{Address: &receiver, CoinType: strPtr("0x2::usdc::USDC"), Amount: strPtr("500")},
		},
	}

	txs := s.convertTransactions(execTx, 1, 1)
	require.Len(t, txs, 1)
	require.Equal(t, actualSender, txs[0].FromAddress)
	require.Equal(t, receiver, txs[0].ToAddress)
	require.Equal(t, "500", txs[0].Amount)
	require.Equal(t, constant.TxTypeTokenTransfer, txs[0].Type)
}

func TestConvertTransactionsKeepsMintToSenderBalanceChange(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sender := "0xsender"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("mint-to-sender"),
		Transaction: &v2.Transaction{
			Sender: &sender,
		},
		BalanceChanges: []*v2.BalanceChange{
			{Address: &sender, CoinType: strPtr("0x2::custom::COIN"), Amount: strPtr("700")},
		},
	}

	txs := s.convertTransactions(execTx, 1, 1)
	require.Len(t, txs, 1)
	require.Equal(t, sender, txs[0].ToAddress)
	require.Equal(t, sender, txs[0].FromAddress)
	require.Equal(t, "700", txs[0].Amount)
	require.Equal(t, constant.TxTypeTokenTransfer, txs[0].Type)
}

func TestConvertTransactionsUsesMoveEventsForMovementSemantics(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sender := "0xsender"
	receiver := "0xreceiver"

	mustStruct := func(m map[string]any) *structpb.Value {
		v, err := structpb.NewValue(m)
		require.NoError(t, err)
		return v
	}

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("event-digest"),
		Transaction: &v2.Transaction{
			Sender: &sender,
		},
		Events: &v2.TransactionEvents{
			Events: []*v2.Event{
				{
					EventType: strPtr("0xswap::pool::SwapEvent"),
					Json:      mustStruct(map[string]any{"amount": "42", "coinType": "0x2::usdc::USDC"}),
				},
				{
					Module:    strPtr("router"),
					EventType: strPtr("0xcetus::router::SwapExecutedEvent"),
					Json:      mustStruct(map[string]any{"amount": "77", "coinType": "0x2::sui::SUI"}),
				},
				{
					EventType: strPtr("0xtoken::wallet::Transfer"),
					Json:      mustStruct(map[string]any{"from": sender, "to": receiver, "amount": "9", "coinType": "0x2::custom::COIN"}),
				},
			},
		},
	}

	txs := s.convertTransactions(execTx, 1, 1)
	require.NotEmpty(t, txs)

	var foundSwapMovement bool
	var foundTransferMovement bool
	for _, tx := range txs {
		require.Contains(t, []constant.TxType{constant.TxTypeNativeTransfer, constant.TxTypeTokenTransfer}, tx.Type)
		if tx.ToAddress == receiver && tx.Amount == "9" && tx.AssetAddress == "0x2::custom::COIN" && tx.Type == constant.TxTypeTokenTransfer {
			foundTransferMovement = true
		}
		if tx.ToAddress == sender && tx.Amount == "42" && tx.AssetAddress == "0x2::usdc::USDC" && tx.Type == constant.TxTypeTokenTransfer {
			foundSwapMovement = true
		}
	}

	require.True(t, foundSwapMovement)
	require.True(t, foundTransferMovement)
}

func TestConvertTransactionsUsesValidatorEventsForStake(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	sender := "0xsender"
	validator := "0xvalidator"

	mustStruct := func(m map[string]any) *structpb.Value {
		v, err := structpb.NewValue(m)
		require.NoError(t, err)
		return v
	}

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("stake-event-digest"),
		Transaction: &v2.Transaction{
			Sender: &sender,
		},
		Events: &v2.TransactionEvents{
			Events: []*v2.Event{
				{
					Module:    strPtr("validator"),
					EventType: strPtr("0x3::validator::StakingRequestEvent"),
					Json: mustStruct(map[string]any{
						"amount":            "13000000000",
						"staker_address":    sender,
						"validator_address": validator,
					}),
				},
			},
		},
	}

	txs := s.convertTransactions(execTx, 1, 1)
	require.Len(t, txs, 1)
	require.Equal(t, constant.TxTypeNativeTransfer, txs[0].Type)
	require.Equal(t, suiNativeCoinType, txs[0].AssetAddress)
	require.Equal(t, "13000000000", txs[0].Amount)
	require.Equal(t, validator, txs[0].ToAddress)
}

func TestConvertTransactionsIgnoresSystemEpochTransactions(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	systemSender := "0x0000000000000000000000000000000000000000000000000000000000000000"

	execTx := &v2.ExecutedTransaction{
		Digest: strPtr("system-epoch-digest"),
		Transaction: &v2.Transaction{
			Sender: &systemSender,
		},
		BalanceChanges: []*v2.BalanceChange{
			{Address: &systemSender, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("-1")},
		},
	}

	require.Nil(t, s.convertTransactions(execTx, 1, 1))
}

func TestConvertCheckpointSetsBlockHashOnTransactions(t *testing.T) {
	t.Parallel()

	s := &SuiIndexer{
		cfg: config.ChainConfig{InternalCode: "sui_mainnet"},
	}

	seq := uint64(42)
	digest := "checkpoint-digest"
	prevDigest := "prev-digest"
	sender := "0xsender"
	receiver := "0xreceiver"

	cp := &sui.Checkpoint{
		Checkpoint: &v2.Checkpoint{
			SequenceNumber: &seq,
			Digest:         &digest,
			Summary: &v2.CheckpointSummary{
				PreviousDigest: &prevDigest,
				Timestamp:      &timestamppb.Timestamp{Seconds: 1710000000},
			},
			Transactions: []*v2.ExecutedTransaction{
				{
					Digest: strPtr("tx-digest"),
					Transaction: &v2.Transaction{
						Sender: &sender,
					},
					BalanceChanges: []*v2.BalanceChange{
						{Address: &sender, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("-10")},
						{Address: &receiver, CoinType: strPtr("0x2::sui::SUI"), Amount: strPtr("10")},
					},
				},
			},
		},
	}

	block := s.convertCheckpoint(cp)
	require.NotNil(t, block)
	require.Len(t, block.Transactions, 1)
	require.Equal(t, digest, block.Transactions[0].BlockHash)
}

func TestClassifyMoveCallAvoidsGenericExchangeFalsePositive(t *testing.T) {
	t.Parallel()

	mc := &v2.MoveCall{
		Package:  strPtr("0xapp"),
		Module:   strPtr("vault"),
		Function: strPtr("exchange_rate_update"),
	}

	require.Empty(t, classifyMoveCall(mc))
}

type typesFixture struct {
	To     string
	Asset  string
	Amount string
}

func TestSuiMainnetFetchAndParseTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	rpcURL := "fullnode.mainnet.sui.io:443"

	testCases := []struct {
		name   string
		digest string
		want   constant.TxType
	}{
		{
			name:   "native transfer",
			digest: strings.TrimSpace("6uz8Evkkg5bzGC1P7omvzCdBwFSzeL6jHs7yTH1rgmWa"),
			want:   constant.TxTypeNativeTransfer,
		},
		{
			name:   "token transfer",
			digest: strings.TrimSpace("EWUh7soPhZk7sBCJQXZHn7fBkbk4gHPsePSKkHu9TBd6"),
			want:   constant.TxTypeTokenTransfer,
		},
		{
			name:   "swap movement",
			digest: strings.TrimSpace("9ygJwFE8zJ6jS6Qj2v4cDhZBzAtFP9t6aa1b89NeQ8vB"),
		},
		{
			name:   "stake movement",
			digest: strings.TrimSpace("7Nno5YH6oM2azMKkBoxAnKz1bVxPknUaUfz4saz8kiP6"),
		},
		{
			name:   "unstake movement",
			digest: strings.TrimSpace("Eu27uF1FMZRuKZnEaQrUUFQYApVeUmkELbU2s4gZJ9af"),
		},
	}

	enabled := false
	for _, tc := range testCases {
		if tc.digest != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		t.Skip("hardcoded real mainnet test cases are empty")
	}

	client := sui.NewSuiClient(rpcURL)
	idx := &SuiIndexer{cfg: config.ChainConfig{InternalCode: "sui_mainnet"}}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			tx, err := client.GetTransaction(ctx, tc.digest)
			require.NoError(t, err, "should fetch real mainnet tx for digest %s", tc.digest)
			require.NotNil(t, tx)

			blockNumber := tx.GetCheckpoint()
			ts := uint64(0)
			if tx.GetTimestamp() != nil {
				ts = uint64(tx.GetTimestamp().Seconds)
			}

			parsed := idx.convertTransactions(tx.ExecutedTransaction, blockNumber, ts)
			require.NotEmpty(t, parsed, "should classify at least one semantic event from mainnet tx")

			if tc.want != "" {
				found := false
				for _, item := range parsed {
					if item.Type == tc.want {
						found = true
						break
					}
				}
				require.True(t, found, "expected tx type %s in parsed outputs", tc.want)
			}

			for _, item := range parsed {
				require.Contains(t, []constant.TxType{constant.TxTypeNativeTransfer, constant.TxTypeTokenTransfer}, item.Type)
				require.NotEmpty(t, item.Type)
				require.NotEmpty(t, item.TxHash)
				require.NotEmpty(t, item.FromAddress)
				require.NotEmpty(t, item.ToAddress)
			}
		})
	}
}
