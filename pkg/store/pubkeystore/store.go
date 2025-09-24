package pubkeystore

import (
	"fmt"

	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/kvstore"
)

func composeKey(addressType enum.NetworkType, publicKey string) string {
	return fmt.Sprintf("%s/%s", addressType, publicKey)
}

type Store interface {
	Exist(addressType enum.NetworkType, publicKey string) bool
	Save(addressType enum.NetworkType, publicKey string) error
	Close() error
}

type publicKeyStore struct {
	kvstore     infra.KVStore
	bloomFilter addressbloomfilter.WalletAddressBloomFilter
}

func NewPublicKeyStore(
	client infra.KVStore,
	bloomFilter addressbloomfilter.WalletAddressBloomFilter,
) Store {
	return &publicKeyStore{kvstore: client, bloomFilter: bloomFilter}
}

func (s *publicKeyStore) Exist(addressType enum.NetworkType, publicKey string) bool {
	// If the bloom filter returns false, the key definitely doesn't exist.
	if s.bloomFilter != nil && !s.bloomFilter.Contains(publicKey, addressType) {
		return false
	}

	// Since bloom filters may have false positives, or if no bloom filter is available,
	// check the underlying KV store.
	v, err := s.kvstore.GetWithOptions(
		composeKey(addressType, publicKey),
		&kvstore.DefaultCacheOptions,
	)
	if v == "" || err != nil {
		return false
	}

	return true
}

func (s *publicKeyStore) Save(addressType enum.NetworkType, publicKey string) error {
	// Only add to bloom filter if it's available
	if s.bloomFilter != nil {
		s.bloomFilter.Add(publicKey, addressType)
	}
	return s.kvstore.Set(composeKey(addressType, publicKey), "ok")
}

func (s *publicKeyStore) Close() error {
	return s.kvstore.Close()
}
