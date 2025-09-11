package pubkeystore

import (
	"fmt"

	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
)

func composeKey(addressType enum.AddressType, publicKey string) string {
	return fmt.Sprintf("%s/%s", addressType, publicKey)
}

type Store interface {
	Exist(addressType enum.AddressType, publicKey string) bool
	Save(addressType enum.AddressType, publicKey string) error
}

type publicKeyStore struct {
	kvstore     infra.KVStore
	bloomFilter addressbloomfilter.WalletAddressBloomFilter
}

func NewPublicKeyStore(client infra.KVStore, bloomFilter addressbloomfilter.WalletAddressBloomFilter) Store {
	return &publicKeyStore{kvstore: client, bloomFilter: bloomFilter}
}

func (s *publicKeyStore) Exist(addressType enum.AddressType, publicKey string) bool {
	// First check the bloom filter.
	// If the bloom filter returns false, the key definitely doesn't exist.
	if !s.bloomFilter.Contains(publicKey, addressType) {
		return false
	}

	// Since bloom filters may have false positives, check the underlying KV store.
	v, err := s.kvstore.GetWithOptions(composeKey(addressType, publicKey), &kvstore.DefaultCacheOptions)
	if v == "" || err != nil {
		return false
	}

	return true
}

func (s *publicKeyStore) Save(addressType enum.AddressType, publicKey string) error {
	s.bloomFilter.Add(publicKey, addressType)
	return s.kvstore.Set(composeKey(addressType, publicKey), "ok")
}
