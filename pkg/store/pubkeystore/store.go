package pubkeystore

import (
	"fmt"

	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
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
	bloomFilter addressbloomfilter.WalletAddressBloomFilter
}

func NewPublicKeyStore(
	bloomFilter addressbloomfilter.WalletAddressBloomFilter,
) Store {
	return &publicKeyStore{bloomFilter: bloomFilter}
}

func (s *publicKeyStore) Exist(addressType enum.NetworkType, publicKey string) bool {
	if s.bloomFilter == nil {
		return false
	}
	return s.bloomFilter.Contains(publicKey, addressType)
}

func (s *publicKeyStore) Save(addressType enum.NetworkType, publicKey string) error {
	if s.bloomFilter != nil {
		s.bloomFilter.Add(publicKey, addressType)
	}
	return nil
}

func (s *publicKeyStore) Close() error {
	return nil
}
