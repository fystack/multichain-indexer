package bitcoin

// Block represents a Bitcoin block with full transaction details
type Block struct {
	Hash              string        `json:"hash"`
	Height            uint64        `json:"height"`
	PreviousBlockHash string        `json:"previousblockhash"`
	Time              uint64        `json:"time"`
	Tx                []Transaction `json:"tx"`
	Confirmations     uint64        `json:"confirmations"`
	Size              int           `json:"size"`
	Weight            int           `json:"weight"`
}

// Transaction represents a Bitcoin transaction
type Transaction struct {
	TxID     string   `json:"txid"`
	Hash     string   `json:"hash"`      // Witness hash
	Size     int      `json:"size"`
	VSize    int      `json:"vsize"`     // Virtual size (for SegWit)
	Version  int      `json:"version"`
	LockTime uint64   `json:"locktime"`
	Vin      []Input  `json:"vin"`
	Vout     []Output `json:"vout"`
}

// Input represents a transaction input
type Input struct {
	TxID      string     `json:"txid"`              // Previous transaction hash
	Vout      uint32     `json:"vout"`              // Previous output index
	ScriptSig ScriptSig  `json:"scriptSig"`         // Signature script
	Sequence  uint64     `json:"sequence"`          // Sequence number
	Witness   []string   `json:"txinwitness,omitempty"` // Witness data (for SegWit)
	PrevOut   *Output    `json:"prevout,omitempty"` // Previous output (extended API)
}

// Output represents a transaction output
type Output struct {
	Value        float64      `json:"value"` // BTC amount
	N            uint32       `json:"n"`     // Output index
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// ScriptSig represents the signature script
type ScriptSig struct {
	ASM string `json:"asm"`
	Hex string `json:"hex"`
}

// ScriptPubKey represents the pubkey script
type ScriptPubKey struct {
	ASM       string   `json:"asm"`
	Hex       string   `json:"hex"`
	Type      string   `json:"type"` // pubkeyhash, scripthash, witness_v0_keyhash, etc.
	Address   string   `json:"address,omitempty"`   // Single address (extended API)
	Addresses []string `json:"addresses,omitempty"` // Multiple addresses (legacy)
}

// BlockchainInfo represents blockchain information
type BlockchainInfo struct {
	Chain         string `json:"chain"`
	Blocks        uint64 `json:"blocks"`
	Headers       uint64 `json:"headers"`
	BestBlockHash string `json:"bestblockhash"`
}
