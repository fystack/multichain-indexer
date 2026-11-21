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

// MaxSupportedTransactionVersion is the newest transaction format getBlock and getTransaction may return.
// Clusters now include version 1 transactions in most blocks; asking for at most 0 makes the RPC reject the
// whole block ("Transaction version (1) is not supported by the requesting client"). Version 1 parses like
// version 0 for transfers (it adds a transactionConfig field and drops addressTableLookups).
const MaxSupportedTransactionVersion = 1

type GetBlockConfig struct {
	Encoding                       string `json:"encoding"`                       // json | jsonParsed
	TransactionDetails             string `json:"transactionDetails"`             // full
	Rewards                        bool   `json:"rewards"`                        // false
	MaxSupportedTransactionVersion int    `json:"maxSupportedTransactionVersion"` // MaxSupportedTransactionVersion
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

type GetTransactionResult struct {
	Slot        uint64      `json:"slot"`
	// Position in its block; nodes that don't report it leave it nil.
	TransactionIndex *int `json:"transactionIndex"`
	BlockTime   *int64      `json:"blockTime"`
	Meta        *TxnMeta    `json:"meta"`
	Transaction TxnEnvelope `json:"transaction"`
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


// MaxMultipleAccounts is the most pubkeys getMultipleAccounts takes in one call.
const MaxMultipleAccounts = 100

// MaxSignaturesPerPage is the most signatures getSignaturesForAddress returns in one call.
const MaxSignaturesPerPage = 1000

type GetMultipleAccountsResult struct {
	Context struct {
		Slot uint64 `json:"slot"`
	} `json:"context"`
	// One entry per requested pubkey, in order; nil when the account doesn't exist.
	Value []*AccountInfo `json:"value"`
}

type AccountInfo struct {
	Lamports uint64   `json:"lamports"`
	Owner    string   `json:"owner"`
	Data     []string `json:"data"` // [base64, "base64"]
}

type SignaturesOptions struct {
	Before string // page back from this signature (exclusive)
	Until  string // stop at this signature (exclusive)
	Limit  int
}

type SignatureInfo struct {
	Signature string `json:"signature"`
	Slot      uint64 `json:"slot"`
	Err       any    `json:"err"`
	BlockTime *int64 `json:"blockTime"`
}
