package cardano

// Block represents a Cardano block
type Block struct {
	Hash       string        `json:"hash"`
	Height     uint64        `json:"height"`
	Slot       uint64        `json:"slot"`
	Time       uint64        `json:"time"`
	ParentHash string        `json:"parent_hash"`
	Txs        []Transaction `json:"tx_count"`
}

// Transaction represents a Cardano transaction
type Transaction struct {
	Hash     string `json:"hash"`
	Slot     uint64 `json:"slot"`
	BlockNum uint64 `json:"block_height"`
	Inputs   []Input
	Outputs  []Output
	Fee      uint64 `json:"fees"`
}

// Input represents a transaction input
type Input struct {
	Address string `json:"address"`
	Amount  uint64 `json:"amount"`
	TxHash  string `json:"tx_hash"`
	Index   uint32 `json:"output_index"`
}

// Output represents a transaction output
type Output struct {
	Address string `json:"address"`
	Amount  uint64 `json:"amount"`
	Index   uint32 `json:"output_index"`
}

// BlockResponse is the response from block query
type BlockResponse struct {
	Hash       string `json:"hash"`
	Height     uint64 `json:"height"`
	Slot       uint64 `json:"slot"`
	Time       uint64 `json:"time"`
	ParentHash string `json:"parent_hash"`
}

// TransactionResponse is the response from transaction query
type TransactionResponse struct {
	Hash  string `json:"hash"`
	Block struct {
		Height uint64 `json:"height"`
		Time   uint64 `json:"time"`
		Slot   uint64 `json:"slot"`
	} `json:"block"`
	Inputs []struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
		TxHash  string `json:"tx_hash"`
		Index   uint32 `json:"output_index"`
	} `json:"inputs"`
	Outputs []struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
		Index   uint32 `json:"output_index"`
	} `json:"outputs"`
	Fees string `json:"fees"`
}

// BlockTxsResponse is the response for block transactions
type BlockTxsResponse struct {
	Transactions []string `json:"transactions"`
}

