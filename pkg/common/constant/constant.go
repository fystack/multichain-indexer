package constant

import "time"

type TxType string

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

	TxTypeTokenTransfer  TxType = "token_transfer"
	TxTypeNativeTransfer TxType = "native_transfer"

	// Transaction confirmation status
	TxnStatusPending    = "pending"    // 0 confirmations (mempool)
	TxnStatusConfirming = "confirming" // 1-5 confirmations
	TxnStatusConfirmed  = "confirmed"  // 6+ confirmations
)
