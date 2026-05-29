package solana

import "github.com/shopspring/decimal"

const LamportsPerSOL = 1_000_000_000

func FeeLamportsToSOL(lamports uint64) decimal.Decimal {
	return decimal.NewFromInt(int64(lamports)).
		Div(decimal.NewFromInt(LamportsPerSOL))
}
