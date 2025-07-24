package types

type Block struct {
	Number       int64         `json:"number"`
	Hash         string        `json:"hash"`
	ParentHash   string        `json:"parent_hash"`
	Timestamp    int64         `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
}

type Transaction struct {
	Hash             string `json:"hash"`
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`
	GasUsed          int64  `json:"gas_used,omitempty"`
	Status           string `json:"status"`
	BlockNumber      int64  `json:"block_number"`
	BlockHash        string `json:"block_hash"`
	TransactionIndex int    `json:"transaction_index"`
}

type IndexerEvent struct {
	Type      string `json:"type"`
	Chain     string `json:"chain"`
	Data      any    `json:"data"`
	Timestamp int64  `json:"timestamp"`
}
