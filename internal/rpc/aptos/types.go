package aptos

import "encoding/json"

type LedgerInfo struct {
	ChainID             int    `json:"chain_id"`
	Epoch               string `json:"epoch"`
	LedgerVersion       string `json:"ledger_version"`
	OldestLedgerVersion string `json:"oldest_ledger_version"`
	LedgerTimestamp     string `json:"ledger_timestamp"`
	NodeRole            string `json:"node_role"`
	OldestBlockHeight   string `json:"oldest_block_height"`
	BlockHeight         string `json:"block_height"`
	GitHash             string `json:"git_hash"`
}

type Block struct {
	BlockHeight    string        `json:"block_height"`
	BlockHash      string        `json:"block_hash"`
	BlockTimestamp string        `json:"block_timestamp"`
	FirstVersion   string        `json:"first_version"`
	LastVersion    string        `json:"last_version"`
	Transactions   []Transaction `json:"transactions,omitempty"`
}

type Transaction struct {
	Version                 string              `json:"version"`
	Hash                    string              `json:"hash"`
	StateChangeHash         string              `json:"state_change_hash"`
	EventRootHash           string              `json:"event_root_hash"`
	StateCheckpointHash     string              `json:"state_checkpoint_hash,omitempty"`
	GasUsed                 string              `json:"gas_used"`
	Success                 bool                `json:"success"`
	VMStatus                string              `json:"vm_status"`
	AccumulatorRootHash     string              `json:"accumulator_root_hash"`
	Changes                 []json.RawMessage   `json:"changes"`
	Sender                  string              `json:"sender,omitempty"`
	SequenceNumber          string              `json:"sequence_number,omitempty"`
	MaxGasAmount            string              `json:"max_gas_amount,omitempty"`
	GasUnitPrice            string              `json:"gas_unit_price,omitempty"`
	ExpirationTimestampSecs string              `json:"expiration_timestamp_secs,omitempty"`
	Payload                 *TransactionPayload `json:"payload,omitempty"`
	Events                  []Event             `json:"events"`
	Timestamp               string              `json:"timestamp"`
	Type                    string              `json:"type"`
}

type TransactionPayload struct {
	Type          string          `json:"type"`
	Function      string          `json:"function,omitempty"`
	TypeArguments []string        `json:"type_arguments,omitempty"`
	Arguments     []interface{}   `json:"arguments,omitempty"`
	Code          json.RawMessage `json:"code,omitempty"`
}

type Event struct {
	GUID           EventGUID `json:"guid"`
	SequenceNumber string    `json:"sequence_number"`
	Type           string    `json:"type"`
	Data           EventData `json:"data"`
}

type EventGUID struct {
	CreationNumber string `json:"creation_number"`
	AccountAddress string `json:"account_address"`
}

type EventData struct {
	Amount string `json:"amount,omitempty"`
	Store  string `json:"store,omitempty"`
}

type ObjectCore struct {
	AllowUngatedTransfer bool   `json:"allow_ungated_transfer"`
	GUIDCreationNum      string `json:"guid_creation_num"`
	Owner                string `json:"owner"`
}

const (
	EventTypeWithdraw = "0x1::coin::WithdrawEvent"
	EventTypeDeposit  = "0x1::coin::DepositEvent"

	EventTypeFAWithdraw = "0x1::fungible_asset::Withdraw"
	EventTypeFADeposit  = "0x1::fungible_asset::Deposit"

	PayloadTypeEntryFunction = "entry_function_payload"
	PayloadTypeScript        = "script_payload"

	TxTypeUser               = "user_transaction"
	TxTypeBlockMetadata      = "block_metadata_transaction"
	TxTypeStateCheckpoint    = "state_checkpoint_transaction"
	TxTypeGenesisTransaction = "genesis_transaction"
)
