package kvstore

import (
	"fmt"

	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/hashicorp/consul/api"
)

// NewFromConfig constructs an infra.KVStore based on kvstore configuration.
func NewFromConfig(cfg config.KVSConfig) (infra.KVStore, error) {
	switch cfg.Type {
	case enum.KVStoreTypeBadger:
		return NewBadgerStore(cfg.Badger.Directory, cfg.Badger.Prefix, infra.JSON)
	case enum.KVStoreTypeConsul:
		return NewConsulClient(Options{
			Scheme:  cfg.Consul.Scheme,
			Address: cfg.Consul.Address,
			Folder:  cfg.Consul.Folder,
			Codec:   infra.JSON,
			Token:   cfg.Consul.Token,
			HttpAuth: &api.HttpBasicAuth{
				Username: cfg.Consul.HttpAuth.Username,
				Password: cfg.Consul.HttpAuth.Password,
			},
		})
	default:
		return nil, fmt.Errorf("unsupported kvstore type: %s", cfg.Type)
	}
}
