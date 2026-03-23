package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/shopspring/decimal"
)

type Block struct {
	Number       uint64                 `json:"number"`
	Hash         string                 `json:"hash"`
	ParentHash   string                 `json:"parent_hash"`
	Timestamp    uint64                 `json:"timestamp"`
	Transactions []Transaction          `json:"transactions"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

func (b *Block) SetMetadata(key string, value interface{}) {
	if b.Metadata == nil {
		b.Metadata = make(map[string]interface{})
	}
	b.Metadata[key] = value
}

func (b *Block) GetMetadata(key string) (interface{}, bool) {
	if b.Metadata == nil {
		return nil, false
	}
	val, ok := b.Metadata[key]
	return val, ok
}

// Transfer direction constants for two-way indexing.
const (
	DirectionIn  = "in"  // Transfer received by a monitored address (deposit)
	DirectionOut = "out" // Transfer sent from a monitored address (withdrawal)
)

type Transaction struct {
	TxHash        string          `json:"txHash"`
	NetworkId     string          `json:"networkId"`
	BlockNumber   uint64          `json:"blockNumber"` // 0 for mempool transactions
	BlockHash     string          `json:"blockHash"`     // block hash for reorg-aware idempotency
	TransferIndex string          `json:"transferIndex"` // unique position within tx (EVM only)
	FromAddress   string          `json:"fromAddress"`
	FromAddresses []string        `json:"fromAddresses,omitempty"`
	ToAddress     string          `json:"toAddress"`
	AssetAddress  string          `json:"assetAddress"`
	Amount        string          `json:"amount"`
	Type          constant.TxType `json:"type"`
	TxFee         decimal.Decimal `json:"txFee"`
	Timestamp     uint64          `json:"timestamp"`
	Confirmations uint64          `json:"confirmations"` // Number of confirmations (0 = mempool/unconfirmed)
	Status        string          `json:"status"`        // "pending" (0 conf), "confirmed" (1+ conf)
	Direction     string          `json:"direction"`     // "in" (deposit) or "out" (withdrawal)
	Metadata      map[string]any  `json:"metadata,omitempty"`
}

func (t *Transaction) SetMetadata(key string, value any) {
	if t.Metadata == nil {
		t.Metadata = make(map[string]any)
	}
	t.Metadata[key] = value
}

func (t Transaction) GetMetadata(key string) (any, bool) {
	if t.Metadata == nil {
		return nil, false
	}
	val, ok := t.Metadata[key]
	return val, ok
}

func (t *Transaction) SetMetadataString(key, value string) {
	if value == "" {
		return
	}
	t.SetMetadata(key, value)
}

func (t Transaction) GetMetadataString(key string) string {
	val, ok := t.GetMetadata(key)
	if !ok {
		return ""
	}
	s, _ := val.(string)
	return s
}

// AllSenderAddresses returns all sender addresses for this transaction.
// For multi-input Bitcoin transactions this returns all input addresses.
// For single-sender chains it falls back to a slice containing FromAddress.
func (t Transaction) AllSenderAddresses() []string {
	if len(t.FromAddresses) > 0 {
		return t.FromAddresses
	}
	if t.FromAddress != "" {
		return []string{t.FromAddress}
	}
	return nil
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
		"{TxHash: %s, NetworkId: %s, BlockNumber: %d, FromAddress: %s, ToAddress: %s, AssetAddress: %s, Amount: %s, Type: %s, TxFee: %s, Timestamp: %d, Confirmations: %d, Status: %s, Direction: %s}",
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
		t.Direction,
	)
}

// Hash generates a deterministic hash used as the NATS idempotency key (Event Instance Identity).
//
// When TransferIndex is set (EVM): NetworkId|TxHash|BlockHash|TransferIndex|Direction
// When TransferIndex is empty (non-EVM fallback): NetworkId|TxHash|BlockHash|From|To|Timestamp|Direction
//
// BlockHash ensures reorgs produce new hashes so consumers get updated data.
// Direction ensures two-way-indexed in/out events don't collide.
func (t Transaction) Hash() string {
	var builder strings.Builder
	builder.WriteString(t.NetworkId)
	builder.WriteByte('|')
	builder.WriteString(t.TxHash)
	builder.WriteByte('|')
	builder.WriteString(t.BlockHash)
	builder.WriteByte('|')

	if t.TransferIndex != "" {
		// EVM (or any chain that populates TransferIndex):
		// Exact positional identity — reorg-aware via BlockHash.
		builder.WriteString(t.TransferIndex)
		builder.WriteByte('|')
		builder.WriteString(t.Direction)
	} else {
		// Non-EVM fallback: preserve current hash behavior.
		// Uses addresses + timestamp to distinguish multiple transfers
		// from the same tx until that chain populates TransferIndex.
		builder.WriteString(t.FromAddress)
		builder.WriteByte('|')
		builder.WriteString(t.ToAddress)
		builder.WriteByte('|')
		builder.WriteString(strconv.FormatUint(t.Timestamp, 10))
		builder.WriteByte('|')
		builder.WriteString(t.Direction)
	}

	hash := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", hash)
}

// Transaction status constants
const (
	StatusPending   = "pending"   // Less than required confirmations
	StatusConfirmed = "confirmed" // Meets or exceeds required confirmations
	StatusOrphaned  = "orphaned"  // Transaction was in a reorged block
)

// IsFinalized returns true if the transaction status is final (confirmed or orphaned)
func IsFinalized(status string) bool {
	return status == StatusConfirmed || status == StatusOrphaned
}
