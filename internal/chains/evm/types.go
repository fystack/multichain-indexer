package evm

import "encoding/json"

// NativeAssetAddress represents the native token address (ETH, BNB, etc.)
var NativeAssetAddress = "0x0000000000000000000000000000000000000000"

// ERC20TransferSig represents the ERC-20 Transfer event signature
var ERC20TransferSig = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// rawBlock represents a raw block from the EVM API
type rawBlock struct {
	Hash          string  `json:"hash"`
	ParentHash    string  `json:"parentHash"`
	Number        string  `json:"number"`                  // Hex string: "0x..."
	Timestamp     string  `json:"timestamp"`               // Hex string: "0x..."
	GasUsed       string  `json:"gasUsed"`                 // Hex string
	GasLimit      string  `json:"gasLimit"`                // Hex string
	BaseFeePerGas string  `json:"baseFeePerGas,omitempty"` // Hex, optional (London+)
	Miner         string  `json:"miner"`
	Transactions  []rawTx `json:"transactions"`
}

// rawTx represents a raw transaction from the EVM API
type rawTx struct {
	Hash        string   `json:"hash"`           // tx hash
	BlockNumber string   `json:"blockNumber"`    // block number
	From        string   `json:"from"`           // sender
	To          string   `json:"to"`             // recipient (might be contract)
	Value       string   `json:"value"`          // value (hex)
	Input       string   `json:"input"`          // data (to distinguish call contract or transfer)
	Gas         string   `json:"gas"`            // gas limit (hex)
	GasPrice    string   `json:"gasPrice"`       // gas price (hex)
	Logs        []rawLog `json:"logs,omitempty"` // transaction receipt logs
}

// rawLog represents a transaction log entry
type rawLog struct {
	Address string   `json:"address"` // contract address that emitted the log
	Topics  []string `json:"topics"`  // indexed parameters
	Data    string   `json:"data"`    // non-indexed parameters (hex)
}

// rawReceipt represents a transaction receipt
type rawReceipt struct {
	TransactionHash string   `json:"transactionHash"`
	BlockNumber     string   `json:"blockNumber"`
	GasUsed         string   `json:"gasUsed"`
	Status          string   `json:"status"`
	Logs            []rawLog `json:"logs"`
}

// RpcRequest represents a JSON-RPC request
type RpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

// RpcResponse represents a JSON-RPC response
type RpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	ID int `json:"id"`
}
