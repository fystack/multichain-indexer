package cardano

// RichAsset represents a single token or native currency in a transaction output.
type RichAsset struct {
	Unit     string `json:"unit"`
	Quantity string `json:"quantity"`
}

// RichOutput represents a destination for funds in a transaction, containing multiple assets.
type RichOutput struct {
	Address string      `json:"address"`
	Assets  []RichAsset `json:"assets"`
}

// RichTransaction is a special structure for Cardano transactions that preserves
// the UTXO model's multi-output/multi-asset nature. The BaseWorker will use
// a type assertion to handle this structure specifically.
type RichTransaction struct {
	Hash        string       `json:"hash"`
	BlockHeight uint64       `json:"block_height"`
	BlockHash   string       `json:"block_hash"`
	Outputs     []RichOutput `json:"outputs"`
	Fee         string       `json:"fee"`
	Chain       string       `json:"chain"`
}

// IsRichTransaction is a marker method to identify this special transaction type.
// This allows the BaseWorker to perform a type assertion without creating a direct dependency.
func (rt *RichTransaction) IsRichTransaction() bool {
	return true
}

