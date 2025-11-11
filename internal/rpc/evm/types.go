package evm

import (
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
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
		Status            string `json:"status"`
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

func (l Log) parseERC20Transfers(
	fee decimal.Decimal,
	txHash, network string,
	blockNumber, ts uint64,
) ([]types.Transaction, error) {
	if len(l.Topics) < 3 || l.Topics[0] != ERC20_TRANSFER_TOPIC {
		return nil, nil
	}

	from := "0x" + l.Topics[1][len(l.Topics[1])-40:]
	from = ToChecksumAddress(from)
	to := "0x" + l.Topics[2][len(l.Topics[2])-40:]
	to = ToChecksumAddress(to)
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
		AssetAddress: ToChecksumAddress(l.Address),
		Amount:       amount.String(),
		Type:         "erc20_transfer",
		TxFee:        fee,
		Timestamp:    ts,
	}
	return []types.Transaction{transfer}, nil
}

func (r *TxnReceipt) IsSuccessful() bool {
	if r == nil {
		return false
	}
	// eth_getTransactionReceipt didn’t always return a status—that field was added post-Byzantium and
	//   some RPC providers (or archive data) still omit it. If we treated an empty string as failure, we’d
	//   end up discarding perfectly good transfers from providers that don’t send the flag. The guard at
	//   internal/rpc/evm/types.go:89 assumes “missing means the node didn’t supply it, not that the tx
	//   failed,” so we keep the previous behavior unless the receipt explicitly says 0x0.
	logger.Info("receipt status", "status", r.Status)
	if r.Status == "" {
		return true
	}
	status, err := utils.ParseHexUint64(r.Status)
	if err != nil {
		return true
	}
	return status != 0
}
