package constant

const (
	EnvProduction  = "production"
	EnvDevelopment = "development"
	// For fresh starts with large block numbers, limit catchup range
	MaxCatchupBlocks = 100000

	KVPrefixLatestBlock     = "latest_block"
	KVPrefixProgressCatchup = "catchup_progress"
	KVPrefixFailedBlocks    = "failed_blocks"
)
