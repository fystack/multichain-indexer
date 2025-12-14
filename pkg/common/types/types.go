package types

import (
	"crypto/sha256"
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
	return fmt.Sprintf(
		"{TxHash: %s, NetworkId: %s, BlockNumber: %d, FromAddress: %s, ToAddress: %s, AssetAddress: %s, Amount: %s, Type: %s, TxFee: %s, Timestamp: %d}",
		t.TxHash,
		t.NetworkId,
		t.BlockNumber,
		t.FromAddress,
		t.ToAddress,
		t.AssetAddress,
		t.Amount,
		t.Type,
		t.TxFee,
		t.Timestamp,
	)
}

// Hash generates a deterministic hash for the transaction that can be used as an idempotent key.
// It combines NetworkID, TxHash, FromAddress, ToAddress, and Timestamp to ensure uniqueness.
func (t Transaction) Hash() string {
	var builder strings.Builder
	builder.WriteString(t.NetworkId)
	builder.WriteByte('|')
	builder.WriteString(t.TxHash)
	builder.WriteByte('|')
	builder.WriteString(t.FromAddress)
	builder.WriteByte('|')
	builder.WriteString(t.ToAddress)
	builder.WriteByte('|')
	builder.WriteString(strconv.FormatUint(t.Timestamp, 10))
	hash := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", hash)
}
