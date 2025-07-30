package evm

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

type rawTx struct {
	Hash        string `json:"hash"`        // tx hash
	BlockNumber string `json:"blockNumber"` // block number
	From        string `json:"from"`        // sender
	To          string `json:"to"`          // recipient (might be contract)
	Value       string `json:"value"`       // value (hex)
	Input       string `json:"input"`       // data (to distinguish call contract or transfer)
}
