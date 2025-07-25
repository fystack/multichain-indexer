package evm

type rawBlock struct {
	Hash         string  `json:"hash"`
	ParentHash   string  `json:"parentHash"`
	Number       string  `json:"number"`
	Timestamp    string  `json:"timestamp"`
	Transactions []rawTx `json:"transactions"`
}

type rawTx struct {
	Hash  string `json:"hash"`
	From  string `json:"from"`
	To    string `json:"to"`
	Value string `json:"value"`
}
