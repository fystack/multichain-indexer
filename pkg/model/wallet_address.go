package model

import (
	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

// WalletAddress represents a monitored wallet address.
//
// IMPORTANT: For optimal BloomSyncWorker performance at scale (1M+ addresses),
// ensure this composite index exists:
//
//	CREATE INDEX CONCURRENTLY idx_wallet_addresses_type_created
//	ON wallet_addresses (type, created_at);
type WalletAddress struct {
	BaseModel
	Address  string               `gorm:"not null;type:varchar(255);uniqueIndex:idx_unique_address" json:"address"`
	Type     enum.NetworkType     `gorm:"type:address_type;not null;index:idx_type"                 json:"type"`
	Standard enum.AddressStandard `                                                                 json:"standard"`
}
