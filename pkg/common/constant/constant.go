package constant

import "time"

const (
	EnvProduction  = "production"
	EnvDevelopment = "development"
	// For fresh starts with large block numbers, limit catchup range
	MaxCatchupBlocks           = 100000
	DefaultReorgRollbackWindow = 50

	KVPrefixLatestBlock     = "latest_block"
	KVPrefixProgressCatchup = "catchup_progress"
	KVPrefixFailedBlocks    = "failed_blocks"
	KVPrefixBlockHash       = "block_hash"

	RangeProcessingTimeout = 3 * time.Minute

	TxnTypeTransfer      = "transfer"
	TxnTypeTRC10Transfer = "trc10_transfer"
	TxnTypeERC20Transfer = "erc20_transfer"
)
