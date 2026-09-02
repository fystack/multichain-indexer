package solana

import "encoding/json"

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
	// LoadedAddresses carries the accounts a versioned (v0) transaction pulls in
	// via Address Lookup Tables. With encoding=json these are NOT included in
	// message.accountKeys, so the full account list used for index resolution is
	// static accountKeys + Writable + Readonly (in that order). With
	// encoding=jsonParsed the RPC already merges them into accountKeys and this
	// field is empty.
	LoadedAddresses *LoadedAddresses `json:"loadedAddresses"`
}

type LoadedAddresses struct {
	Writable []string `json:"writable"`
	Readonly []string `json:"readonly"`
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
	BlockTime   *int64      `json:"blockTime"`
	Meta        *TxnMeta    `json:"meta"`
	Transaction TxnEnvelope `json:"transaction"`
}

type AccountKey struct {
	Pubkey   string `json:"pubkey"`
	Signer   bool   `json:"signer"`
	Writable bool   `json:"writable"`
}

// UnmarshalJSON accepts both encodings of message.accountKeys:
//   - encoding=json:       a bare base58 pubkey string
//   - encoding=jsonParsed: an object { pubkey, signer, source, writable }
func (a *AccountKey) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var pubkey string
		if err := json.Unmarshal(data, &pubkey); err != nil {
			return err
		}
		a.Pubkey = pubkey
		return nil
	}
	type alias AccountKey
	var v alias
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*a = AccountKey(v)
	return nil
}

type Instruction struct {
	ProgramIdIndex uint64 `json:"programIdIndex"`
	Accounts       any    `json:"accounts"`
	Data           string `json:"data"` // base58 (when encoding=json); may be "" for jsonParsed
	Parsed         any    `json:"parsed"` // object for jsonParsed, can be "" for encoding=json
	Program        string `json:"program"`
	ProgramId      string `json:"programId"`
}

