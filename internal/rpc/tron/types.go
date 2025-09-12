package tron

import "encoding/json"

type ContractType string

const (
	ContractTypeTransfer             ContractType = "TransferContract"
	ContractTypeTransferAsset        ContractType = "TransferAssetContract"
	ContractTypeTriggerSmartContract ContractType = "TriggerSmartContract"
)

type (
	Block struct {
		BlockID      string      `json:"blockID"`
		BlockHeader  BlockHeader `json:"block_header"`
		Transactions []Txn       `json:"transactions"`
	}

	BlockHeader struct {
		RawData BlockRawData `json:"raw_data"`
	}

	BlockRawData struct {
		Number     int64  `json:"number"`
		Timestamp  int64  `json:"timestamp"`
		ParentHash string `json:"parentHash"`
	}

	Txn struct {
		TxID    string     `json:"txID"`
		RawData TxnRawData `json:"raw_data"`
		Ret     []TxnRet   `json:"ret"`
	}

	TxnRawData struct {
		Contract  []Contract `json:"contract"`
		Timestamp int64      `json:"timestamp"`
	}

	TxnRet struct {
		ContractRet string `json:"contractRet"`
		Fee         int64  `json:"fee"`
	}

	Contract struct {
		Parameter ContractParam `json:"parameter"`
		Type      ContractType  `json:"type"`
	}

	ContractParam struct {
		Value json.RawMessage `json:"value"`
	}

	// Transfer contract types (simplified)
	TransferContract struct {
		OwnerAddress string `json:"owner_address"`
		ToAddress    string `json:"to_address"`
		Amount       int64  `json:"amount"`
	}

	TransferAssetContract struct {
		OwnerAddress string `json:"owner_address"`
		ToAddress    string `json:"to_address"`
		AssetName    string `json:"asset_name"`
		Amount       int64  `json:"amount"`
	}

	TriggerSmartContract struct {
		OwnerAddress    string `json:"owner_address"`
		ContractAddress string `json:"contract_address"`
		Data            string `json:"data"`
	}

	// Simplified transaction info for transfer analysis
	TxnInfo struct {
		ID               string  `json:"id"`
		Fee              int64   `json:"fee"`
		BlockNumber      int64   `json:"blockNumber"`
		BlockTimestamp   int64   `json:"blockTimeStamp"`
		Log              []Log   `json:"log"`
		Receipt          Receipt `json:"receipt"`
		Result           string  `json:"result"`
		ResMessage       string  `json:"resMessage"`
		EnergyFee        int64   `json:"energy_fee"`
		EnergyUsageTotal int64   `json:"energy_usage_total"`
		NetFee           int64   `json:"net_fee"`
		NetUsage         int64   `json:"net_usage"`
	}

	Receipt struct {
		EnergyUsage        int64  `json:"energy_usage"`
		EnergyUsageTotal   int64  `json:"energy_usage_total"`
		NetUsage           int64  `json:"net_usage"`
		Result             string `json:"result"`
		EnergyFee          int64  `json:"energy_fee"`
		NetFee             int64  `json:"net_fee"`
		EnergyPenaltyTotal int64  `json:"energy_penalty_total"`
	}

	Log struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	}
)
