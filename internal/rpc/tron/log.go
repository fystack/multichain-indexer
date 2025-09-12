package tron

import (
	"math/big"
	"strings"

	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const TRC20_TRANSFER_TOPIC = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

func (l Log) ParseTRC20Transfers(
	txID, network string,
	blockNum, ts uint64,
) ([]types.Transaction, error) {
	if len(l.Topics) < 3 ||
		strings.ToLower(strings.TrimPrefix(l.Topics[0], "0x")) != TRC20_TRANSFER_TOPIC {
		return nil, nil
	}
	from := "0x" + l.Topics[1][len(l.Topics[1])-40:]
	to := "0x" + l.Topics[2][len(l.Topics[2])-40:]
	amount := new(big.Int)
	amount.SetString(strings.TrimPrefix(l.Data, "0x"), 16)

	tr := types.Transaction{
		TxHash:       txID,
		NetworkId:    network,
		BlockNumber:  blockNum,
		FromAddress:  from,
		ToAddress:    to,
		AssetAddress: l.Address,
		Amount:       amount.String(),
		Type:         "erc20_transfer",
		TxFee:        decimal.Zero,
		Timestamp:    ts,
	}
	return []types.Transaction{tr}, nil
}
