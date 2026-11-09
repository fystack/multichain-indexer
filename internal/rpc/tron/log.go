package tron

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

const TRC20_TRANSFER_TOPIC = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// LogTransferIndex identifies a TRC-20 transfer by its log position within the tx.
// Tron receipts carry no log index field, so the slice position is the stable index.
func LogTransferIndex(logIndex int) string {
	return "log:" + strconv.Itoa(logIndex)
}

// ContractTransferIndex identifies a TRX/TRC-10 transfer by its contract position within the tx.
func ContractTransferIndex(contractIndex int) string {
	return "contract:" + strconv.Itoa(contractIndex)
}

func (l Log) ParseTRC20Transfers(
	txID, network, blockHash string,
	blockNum, ts uint64,
	logIndex int,
) ([]types.Transaction, error) {
	if len(l.Topics) < 3 ||
		strings.ToLower(strings.TrimPrefix(l.Topics[0], "0x")) != TRC20_TRANSFER_TOPIC {
		return nil, nil
	}
	from := "41" + l.Topics[1][len(l.Topics[1])-40:]
	to := "41" + l.Topics[2][len(l.Topics[2])-40:]
	amount := new(big.Int)
	amount.SetString(strings.TrimPrefix(l.Data, "0x"), 16)

	tr := types.Transaction{
		TxHash:        txID,
		NetworkId:     network,
		BlockNumber:   blockNum,
		BlockHash:     blockHash,
		TransferIndex: LogTransferIndex(logIndex),
		FromAddress:   HexToTronAddress(from),
		ToAddress:     HexToTronAddress(to),
		AssetAddress:  HexToTronAddress(l.Address),
		Amount:        amount.String(),
		Type:          constant.TxTypeTokenTransfer,
		TxFee:         decimal.Zero,
		Timestamp:     ts,
	}
	return []types.Transaction{tr}, nil
}
