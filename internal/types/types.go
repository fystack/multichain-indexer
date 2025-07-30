package types

import (
	"math/big"
	"strconv"
	"strings"
)

type Block struct {
	Number       uint64        `json:"number"`
	Hash         string        `json:"hash"`
	ParentHash   string        `json:"parent_hash"`
	Timestamp    uint64        `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
}

type Transaction struct {
	Hash             string   `json:"hash"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	Value            *big.Int `json:"value"` // parsed from hex
	GasUsed          int64    `json:"gas_used,omitempty"`
	Status           bool     `json:"status"` // true if "0x1", false if "0x0"
	BlockNumber      uint64   `json:"block_number"`
	BlockHash        string   `json:"block_hash"`
	TransactionIndex int      `json:"transaction_index"`
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
