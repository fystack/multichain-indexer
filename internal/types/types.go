package types

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

type Block struct {
	Number       uint64        `json:"number"`
	Hash         string        `json:"hash"`
	ParentHash   string        `json:"parent_hash"`
	Timestamp    uint64        `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
}

type Transaction struct {
	TxHash       string          `json:"txHash"`
	NetworkId    string          `json:"networkId"`
	BlockNumber  uint64          `json:"blockNumber"`
	FromAddress  string          `json:"fromAddress"`
	ToAddress    string          `json:"toAddress"`
	AssetAddress string          `json:"assetAddress"`
	Amount       string          `json:"amount"`
	Type         string          `json:"type"`
	TxFee        decimal.Decimal `json:"txFee"`
	Timestamp    uint64          `json:"timestamp"`
}

func (t Transaction) MarshalBinary() ([]byte, error) {
	bytes, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (t *Transaction) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, &t)
}

func (t Transaction) String() string {
	return fmt.Sprintf("{TxHash: %s, NetworkId: %s, BlockNumber: %d, FromAddress: %s, ToAddress: %s, AssetAddress: %s, Amount: %s, Type: %s, TxFee: %s, Timestamp: %d}",
		t.TxHash, t.NetworkId, t.BlockNumber, t.FromAddress, t.ToAddress, t.AssetAddress, t.Amount, t.Type, t.TxFee, t.Timestamp)
}

type IndexerEvent struct {
	Type      string `json:"type"`
	Chain     string `json:"chain"`
	Data      any    `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

func ParseUint64(hex string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(hex, "0x"), 16, 64)
}

func ParseInt64(hex string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(hex, "0x"), 16, 64)
}
