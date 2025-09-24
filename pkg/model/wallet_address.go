package model

import (
	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

type WalletAddress struct {
	BaseModel
	Address  string               `gorm:"not null;type:varchar(255);uniqueIndex:idx_unique_address" json:"address"`
	Type     enum.NetworkType     `gorm:"type:address_type;not null"                                json:"type"`
	Standard enum.AddressStandard `                                                                 json:"standard"`
}
