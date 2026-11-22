package stellar

type Root struct {
	HistoryLatestLedger uint64 `json:"history_latest_ledger"`
}

type Ledger struct {
	Hash          string `json:"hash"`
	PrevHash      string `json:"prev_hash"`
	Sequence      uint64 `json:"sequence"`
	ClosedAt      string `json:"closed_at"`
	SuccessfulTxs int    `json:"successful_transaction_count"`
}

type PaymentsPage struct {
	Embedded struct {
		Records []Payment `json:"records"`
	} `json:"_embedded"`
}

type OperationsPage struct {
	Embedded struct {
		Records []Operation `json:"records"`
	} `json:"_embedded"`
}

type EffectsPage struct {
	Embedded struct {
		Records []Effect `json:"records"`
	} `json:"_embedded"`
}

type Payment struct {
	ID                    string `json:"id"`
	PagingToken           string `json:"paging_token"`
	Type                  string `json:"type"`
	TransactionHash       string `json:"transaction_hash"`
	TransactionSuccessful bool   `json:"transaction_successful"`
	From                  string `json:"from"`
	To                    string `json:"to"`
	ToMuxedID             string `json:"to_muxed_id"`
	IntoMuxedID           string `json:"into_muxed_id"`
	Funder                string `json:"funder"`
	Account               string `json:"account"`
	Into                  string `json:"into"`
	SourceAccount         string `json:"source_account"`
	SourceAmount          string `json:"source_amount"`
	SourceAssetType       string `json:"source_asset_type"`
	SourceAssetCode       string `json:"source_asset_code"`
	SourceAssetIssuer     string `json:"source_asset_issuer"`
	Amount                string `json:"amount"`
	StartingBalance       string `json:"starting_balance"`
	AssetType             string `json:"asset_type"`
	AssetCode             string `json:"asset_code"`
	AssetIssuer           string `json:"asset_issuer"`
	CreatedAt             string `json:"created_at"`
}

type Transaction struct {
	Hash       string `json:"hash"`
	Successful bool   `json:"successful"`
	FeeCharged string `json:"fee_charged"`
	Memo       string `json:"memo"`
	MemoType   string `json:"memo_type"`
	Ledger     uint64 `json:"ledger"`
	CreatedAt  string `json:"created_at"`
}

type Operation struct {
	ID                    string     `json:"id"`
	PagingToken           string     `json:"paging_token"`
	Type                  string     `json:"type"`
	TransactionHash       string     `json:"transaction_hash"`
	TransactionSuccessful bool       `json:"transaction_successful"`
	SourceAccount         string     `json:"source_account"`
	CreatedAt             string     `json:"created_at"`
	Asset                 string     `json:"asset,omitempty"`
	Amount                string     `json:"amount,omitempty"`
	From                  string     `json:"from,omitempty"`
	BalanceID             string     `json:"balance_id,omitempty"`
	Claimant              string     `json:"claimant,omitempty"`
	Claimants             []Claimant `json:"claimants,omitempty"`
}

type Claimant struct {
	Destination string `json:"destination"`
}

type Effect struct {
	ID          string `json:"id"`
	PagingToken string `json:"paging_token"`
	Type        string `json:"type"`
	Account     string `json:"account,omitempty"`
	Asset       string `json:"asset,omitempty"`
	Amount      string `json:"amount,omitempty"`
	BalanceID   string `json:"balance_id,omitempty"`
}
