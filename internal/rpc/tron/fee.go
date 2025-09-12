package tron

import "github.com/shopspring/decimal"

const SUN = 1_000_000

// TotalFeeTRX calculates total fee for a txn info
func (ti *TxnInfo) TotalFeeTRX() decimal.Decimal {
	if ti == nil {
		return decimal.Zero
	}
	total := decimal.NewFromInt(ti.Fee + ti.EnergyFee + ti.NetFee)
	if total.IsZero() {
		total = decimal.NewFromInt(ti.Receipt.EnergyFee + ti.Receipt.NetFee)
	}
	if ti.Receipt.EnergyPenaltyTotal > 0 {
		total = total.Add(decimal.NewFromInt(ti.Receipt.EnergyPenaltyTotal))
	}
	return total.Div(decimal.NewFromInt(SUN))
}
