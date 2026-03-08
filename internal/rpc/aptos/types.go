package aptos

import "encoding/json"

// LedgerInfo contains Aptos node metadata from GET /v1.
type LedgerInfo struct {
	ChainID             uint64 `json:"chain_id"`
	Epoch               string `json:"epoch"`
	LedgerVersion       string `json:"ledger_version"`
	OldestLedgerVersion string `json:"oldest_ledger_version"`
	LedgerTimestamp     string `json:"ledger_timestamp"`
	OldestBlockHeight   string `json:"oldest_block_height"`
	BlockHeight         string `json:"block_height"`
	NodeRole            string `json:"node_role"`
}

// BlockResponse represents GET /v1/blocks/by_height/{height}?with_transactions=true.
type BlockResponse struct {
	BlockHeight    string        `json:"block_height"`
	BlockHash      string        `json:"block_hash"`
	BlockTimestamp string        `json:"block_timestamp"`
	FirstVersion   string        `json:"first_version"`
	LastVersion    string        `json:"last_version"`
	Transactions   []Transaction `json:"transactions"`
}

// Transaction is a reduced Aptos transaction model used by the indexer.
type Transaction struct {
	Type         string              `json:"type"`
	Hash         string              `json:"hash"`
	Version      string              `json:"version"`
	Timestamp    string              `json:"timestamp"`
	Success      bool                `json:"success"`
	Sender       string              `json:"sender"`
	GasUsed      string              `json:"gas_used"`
	GasUnitPrice string              `json:"gas_unit_price"`
	Payload      *TransactionPayload `json:"payload"`
}

// TransactionPayload is the entry function payload shape.
type TransactionPayload struct {
	Type          string            `json:"type"`
	Function      string            `json:"function"`
	TypeArguments []string          `json:"type_arguments"`
	Arguments     []json.RawMessage `json:"arguments"`
}
