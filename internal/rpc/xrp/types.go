package xrp

import "encoding/json"

type Ledger struct {
	LedgerHash   string        `json:"ledger_hash"`
	ParentHash   string        `json:"parent_hash"`
	LedgerIndex  json.Number   `json:"ledger_index"`
	CloseTime    int64         `json:"close_time"`
	Transactions []Transaction `json:"transactions"`
}

type Transaction struct {
	Hash            string      `json:"hash"`
	Account         string      `json:"Account"`
	Destination     string      `json:"Destination"`
	DestinationTag  *uint32     `json:"DestinationTag,omitempty"`
	TransactionType string      `json:"TransactionType"`
	Fee             string      `json:"Fee"`
	Date            *int64      `json:"date,omitempty"`
	LedgerIndex     json.Number `json:"ledger_index"`
	Amount          any         `json:"Amount"`
	Meta            *Meta       `json:"meta,omitempty"`
	MetaData        *Meta       `json:"metaData,omitempty"`
}

type Meta struct {
	TransactionResult string `json:"TransactionResult"`
	DeliveredAmount   any    `json:"delivered_amount"`
}

type IssuedCurrencyAmount struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer"`
	Value    string `json:"value"`
}

type ledgerResponse struct {
	Ledger       *Ledger     `json:"ledger"`
	LedgerIndex  json.Number `json:"ledger_index"`
	Validated    bool        `json:"validated"`
	Error        string      `json:"error"`
	ErrorMessage string      `json:"error_message"`
}
