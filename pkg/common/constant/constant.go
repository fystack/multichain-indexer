package constant

const (
	EnvProduction  = "production"
	EnvDevelopment = "development"
	// For fresh starts with large block numbers, limit catchup range
	MaxCatchupBlocks = 100000

	LatestBlockKeyPrefix = "latest_block_"
	ProgressKeyPrefix    = "catchup_progress_"
)
