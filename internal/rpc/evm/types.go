package evm

import (
	"strings"

	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
)

const (
	ERC20_TRANSFER_TOPIC    = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	ERC20_TRANSFER_SIG      = "0xa9059cbb"
	ERC20_TRANSFER_FROM_SIG = "0x23b872dd"
)

type (
	Block struct {
		Number       string `json:"number"`
		Hash         string `json:"hash"`
		ParentHash   string `json:"parentHash"`
		Timestamp    string `json:"timestamp"`
		Transactions []Txn  `json:"transactions"`
	}

	Txn struct {
		Hash        string `json:"hash"`
		From        string `json:"from"`
		To          string `json:"to"`
		Value       string `json:"value"`
		Input       string `json:"input"`
		Gas         string `json:"gas"`
		GasPrice    string `json:"gasPrice"`
		BlockNumber string `json:"blockNumber"`
	}

	TxnReceipt struct {
		TransactionHash   string `json:"transactionHash"`
		GasUsed           string `json:"gasUsed"`
		EffectiveGasPrice string `json:"effectiveGasPrice"`
		Logs              []Log  `json:"logs"`
	}

	Log struct {
		Address         string   `json:"address"`
		Topics          []string `json:"topics"`
		Data            string   `json:"data"`
		BlockNumber     string   `json:"blockNumber"`
		TransactionHash string   `json:"transactionHash"`
		LogIndex        string   `json:"logIndex"`
	}
)

func (b *Block) CollectTxNeedingReceipts() []string {
	all := []string{}
	for _, tx := range b.Transactions {
		if len(strings.TrimSpace(tx.Input)) > 2 {
			all = append(all, tx.Hash)
		}
	}
	return all
}

func (l Log) parseERC20Transfers(
	txHash, network string,
	blockNumber, ts uint64,
) ([]types.Transaction, error) {
	if len(l.Topics) < 3 || l.Topics[0] != ERC20_TRANSFER_TOPIC {
		return nil, nil
	}

	from := "0x" + l.Topics[1][len(l.Topics[1])-40:]
	to := "0x" + l.Topics[2][len(l.Topics[2])-40:]
	amount, err := utils.ParseHexBigInt(l.Data)
	if err != nil {
		return nil, err
	}

	transfer := types.Transaction{
		TxHash:       txHash,
		NetworkId:    network,
		BlockNumber:  blockNumber,
		FromAddress:  from,
		ToAddress:    to,
		AssetAddress: l.Address,
		Amount:       amount.String(),
		Type:         "erc20_transfer",
		TxFee:        decimal.Zero,
		Timestamp:    ts,
	}
	return []types.Transaction{transfer}, nil
}
