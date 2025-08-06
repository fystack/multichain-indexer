package tron

import (
	"encoding/json"
	"fmt"
)

// Contract types supported by the Tron network
const (
	TransferContractType      = "TransferContract"      // Native TRX transfer
	TransferAssetContractType = "TransferAssetContract" // TRC-10 token transfer
	TriggerSmartContractType  = "TriggerSmartContract"  // TRC-20 token transfer
)

// TxnType represents the type of transaction being processed
type TxnType string

// Transaction type constants
const (
	TxnNative TxnType = "NATIVE" // Native TRX transaction
	TxnTRC10  TxnType = "TRC10"  // TRC-10 token transaction
	TxnTRC20  TxnType = "TRC20"  // TRC-20 token transaction
)

func (t TxnType) String() string {
	return string(t)
}

// FlexibleString handles both string and number JSON values from the Tron API
// This is necessary because the Tron API sometimes returns numeric values as strings
// and sometimes as numbers
type FlexibleString string

// UnmarshalJSON implements custom JSON unmarshaling to handle multiple data types
func (fs *FlexibleString) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first (most common case)
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*fs = FlexibleString(s)
		return nil
	}

	// Try to unmarshal as json.Number
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*fs = FlexibleString(n.String())
		return nil
	}

	// Try to unmarshal as int64
	var i int64
	if err := json.Unmarshal(data, &i); err == nil {
		*fs = FlexibleString(fmt.Sprintf("%d", i))
		return nil
	}

	return fmt.Errorf("cannot unmarshal %s into FlexibleString", string(data))
}

func (fs FlexibleString) String() string {
	return string(fs)
}

// ContractValue represents the value field in contract parameters
// It contains fields for different contract types
type ContractValue struct {
	// Common fields for all contract types
	OwnerAddress string `json:"owner_address"`

	// TransferContract (native TRX) fields
	ToAddress string         `json:"to_address,omitempty"`
	Amount    FlexibleString `json:"amount,omitempty"`

	// TransferAssetContract (TRC-10) fields
	AssetName string `json:"asset_name,omitempty"`

	// TriggerSmartContract (TRC-20) fields
	ContractAddress string         `json:"contract_address,omitempty"`
	Data            string         `json:"data,omitempty"`
	CallValue       FlexibleString `json:"call_value,omitempty"`
}

// ContractParameter represents the parameter structure in a contract
type ContractParameter struct {
	Value ContractValue `json:"value"`
}

// Contract represents a single contract in a transaction
type Contract struct {
	Type      string            `json:"type"`
	Parameter ContractParameter `json:"parameter"`
}

// tronTransaction represents a raw transaction from the Tron API
type tronTransaction struct {
	TxID    string `json:"txID"`
	RawData struct {
		Contract []Contract `json:"contract"`
	} `json:"raw_data"`
}

// BlockHeaderRawData represents the raw data in a block header
type BlockHeaderRawData struct {
	Number    int64 `json:"number"`
	Timestamp int64 `json:"timestamp"`
}

// BlockHeader represents a block header
type BlockHeader struct {
	RawData BlockHeaderRawData `json:"raw_data"`
}

// simpleBlockRaw represents the basic block structure from Tron API
type simpleBlockRaw struct {
	BlockID     string      `json:"blockID"`
	BlockHeader BlockHeader `json:"block_header"`
}

// tronBlock represents a complete block with transactions
type tronBlock struct {
	simpleBlockRaw
	Transactions []tronTransaction `json:"transactions"`
}
