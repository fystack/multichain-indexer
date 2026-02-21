package cosmos

type StatusResponse struct {
	SyncInfo SyncInfo `json:"sync_info"`
}

type SyncInfo struct {
	LatestBlockHeight string `json:"latest_block_height"`
}

type BlockResponse struct {
	BlockID BlockID `json:"block_id"`
	Block   Block   `json:"block"`
}

type BlockID struct {
	Hash string `json:"hash"`
}

type Block struct {
	Header BlockHeader `json:"header"`
	Data   BlockData   `json:"data"`
}

type BlockHeader struct {
	Height      string      `json:"height"`
	Time        string      `json:"time"`
	LastBlockID LastBlockID `json:"last_block_id"`
}

type LastBlockID struct {
	Hash string `json:"hash"`
}

type BlockData struct {
	Txs []string `json:"txs"`
}

type BlockResultsResponse struct {
	Height     string     `json:"height"`
	TxsResults []TxResult `json:"txs_results"`
}

type TxResponse struct {
	Hash     string   `json:"hash"`
	Height   string   `json:"height"`
	TxResult TxResult `json:"tx_result"`
}

type TxResult struct {
	Code   uint32  `json:"code"`
	Log    string  `json:"log"`
	Events []Event `json:"events"`
}

type Event struct {
	Type       string           `json:"type"`
	Attributes []EventAttribute `json:"attributes"`
}

type EventAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Index bool   `json:"index,omitempty"`
}
