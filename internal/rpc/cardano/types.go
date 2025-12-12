package cardano

// Block represents a Cardano block
type Block struct {
	Hash       string        `json:"hash"`
	Height     uint64        `json:"height"`
	Slot       uint64        `json:"slot"`
	Time       uint64        `json:"time"`
	ParentHash string        `json:"previous_block"`
	Txs        []Transaction `json:"-"`
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
	Address string   `json:"address"`
	Amounts []Amount `json:"amounts"`
	TxHash  string   `json:"tx_hash"`
	Index   uint32   `json:"output_index"`
}

// Output represents a transaction output
type Output struct {
	Address string   `json:"address"`
	Amounts []Amount `json:"amounts"`
	Index   uint32   `json:"output_index"`
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
	Hash   string `json:"hash"`
	Fees   string `json:"fees"`
	Height uint64 `json:"block_height"`
	Time   uint64 `json:"block_time"`
	Slot   uint64 `json:"slot"`
}

type Amount struct {
	Unit     string `json:"unit"`
	Quantity string `json:"quantity"`
}

type UTxO struct {
	Address     string   `json:"address"`
	Amount      []Amount `json:"amount"`
	TxHash      string   `json:"tx_hash"`
	OutputIndex uint32   `json:"output_index"`
}

type TxUTxOsResponse struct {
	Hash    string `json:"hash"`
	Inputs  []UTxO `json:"inputs"`
	Outputs []UTxO `json:"outputs"`
}

// BlockTxsResponse is the response for block transactions
type BlockTxsResponse struct {
	Transactions []string `json:"transactions"`
}

