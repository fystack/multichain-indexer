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
	TxHash        string          `json:"txHash"`
	NetworkId     string          `json:"networkId"`
	BlockNumber   uint64          `json:"blockNumber"` // 0 for mempool transactions
	FromAddress   string          `json:"fromAddress"`
	ToAddress     string          `json:"toAddress"`
	AssetAddress  string          `json:"assetAddress"`
	Amount        string          `json:"amount"`
	Type          string          `json:"type"`
	TxFee         decimal.Decimal `json:"txFee"`
	Timestamp     uint64          `json:"timestamp"`
	Confirmations uint64          `json:"confirmations"` // Number of confirmations (0 = mempool/unconfirmed)
	Status        string          `json:"status"`        // "pending" (0 conf), "confirmed" (1+ conf)
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
		"{TxHash: %s, NetworkId: %s, BlockNumber: %d, FromAddress: %s, ToAddress: %s, AssetAddress: %s, Amount: %s, Type: %s, TxFee: %s, Timestamp: %d, Confirmations: %d, Status: %s}",
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
		t.Confirmations,
		t.Status,
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

// Transaction status constants
const (
	StatusPending   = "pending"   // 0 confirmations (mempool)
	StatusConfirmed = "confirmed" // 1+ confirmations (txfinalizer handles finality)
	StatusOrphaned  = "orphaned"  // Transaction was in a reorged block
)

// CalculateStatus determines transaction status based on confirmation count
// 0 confirmations: "pending" (mempool)
// 1+ confirmations: "confirmed" (txfinalizer handles finality with 6+ blocks)
// Note: "orphaned" status is set manually when a reorg is detected
func CalculateStatus(confirmations uint64) string {
	if confirmations == 0 {
		return StatusPending
	}
	return StatusConfirmed
}

// IsFinalized returns true if the transaction status is final (confirmed or orphaned)
func IsFinalized(status string) bool {
	return status == StatusConfirmed || status == StatusOrphaned
}
