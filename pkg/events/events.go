package events

// AssetTransfer represents a single asset being transferred in a transaction.
type AssetTransfer struct {
	Unit     string `json:"unit"`     // For ADA: "lovelace". For tokens: policyID or contract address.
	Quantity string `json:"quantity"` // Amount of the asset.
}

// MultiAssetTransactionEvent is a structured event for a transaction that can
// involve multiple assets being sent to a single destination address. This is
// optimized for UTXO chains like Cardano.
type MultiAssetTransactionEvent struct {
	Chain       string          `json:"chain"`
	TxHash      string          `json:"tx_hash"`
	BlockHeight uint64          `json:"block_height"`
	FromAddress string          `json:"from_address"` // Representative from address
	ToAddress   string          `json:"to_address"`   // The address that received the assets
	Assets      []AssetTransfer `json:"assets"`       // List of assets transferred to the ToAddress
	Fee         string          `json:"fee"`
	Timestamp   uint64          `json:"timestamp"`
}

