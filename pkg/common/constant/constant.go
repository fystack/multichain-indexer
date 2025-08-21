package constant

import "time"

const (
	EnvProduction             = "production"
	WeiDecimals               = 18
	AverageEvmTransferGasCost = 80000 // unit

	SolDecimals = 9

	RangeProcessingTimeout = 3 * time.Minute
)
