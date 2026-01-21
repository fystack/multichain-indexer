package solana

// Minimal JSON-RPC types for Solana getBlock / getSlot.

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse[T any] struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Result  T             `json:"result"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type GetSlotResult uint64

type GetBlockConfig struct {
	Encoding                       string `json:"encoding"`                       // json | jsonParsed
	TransactionDetails             string `json:"transactionDetails"`             // full
	Rewards                        bool   `json:"rewards"`                        // false
	MaxSupportedTransactionVersion int    `json:"maxSupportedTransactionVersion"` // 0
	Commitment                     string `json:"commitment,omitempty"`           // processed | confirmed | finalized
}

type GetBlockResult struct {
	Blockhash         string            `json:"blockhash"`
	PreviousBlockhash string            `json:"previousBlockhash"`
	ParentSlot        uint64            `json:"parentSlot"`
	BlockTime         *int64            `json:"blockTime"`
	Transactions      []BlockTxn        `json:"transactions"`
}

type BlockTxn struct {
	Meta        *TxnMeta    `json:"meta"`
	Transaction TxnEnvelope `json:"transaction"`
}

type TxnMeta struct {
	Err              any            `json:"err"`
	Fee              uint64         `json:"fee"`
	PreBalances      []uint64       `json:"preBalances"`
	PostBalances     []uint64       `json:"postBalances"`
	PreTokenBalances []TokenBalance `json:"preTokenBalances"`
	PostTokenBalances []TokenBalance `json:"postTokenBalances"`
	InnerInstructions []InnerInstruction `json:"innerInstructions"`
}

type InnerInstruction struct {
	Index        uint64        `json:"index"`
	Instructions []Instruction `json:"instructions"`
}

type TokenBalance struct {
	AccountIndex uint64 `json:"accountIndex"`
	Mint         string `json:"mint"`
	Owner        string `json:"owner"`
	UiTokenAmount struct {
		Amount   string `json:"amount"`
		Decimals uint8  `json:"decimals"`
	} `json:"uiTokenAmount"`
}

type TxnEnvelope struct {
	Message struct {
		AccountKeys  []AccountKey   `json:"accountKeys"`
		Instructions []Instruction  `json:"instructions"`
	} `json:"message"`
	Signatures []string `json:"signatures"`
}

type AccountKey struct {
	Pubkey   string `json:"pubkey"`
	Signer   bool   `json:"signer"`
	Writable bool   `json:"writable"`
}

type Instruction struct {
	ProgramIdIndex uint64 `json:"programIdIndex"`
	Accounts       any    `json:"accounts"`
	Data           string `json:"data"` // base58 (when encoding=json); may be "" for jsonParsed
	Parsed         any    `json:"parsed"` // object for jsonParsed, can be "" for encoding=json
	Program        string `json:"program"`
	ProgramId      string `json:"programId"`
}

