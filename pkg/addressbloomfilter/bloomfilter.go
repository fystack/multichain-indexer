package addressbloomfilter

import (
	"context"

	"github.com/fystack/transaction-indexer/pkg/common/enum"
)

// WalletAddressBloomFilter defines the interface for working with wallet address filters.
type WalletAddressBloomFilter interface {
	// Initialize fully resets the bloom filter from database state.
	Initialize(ctx context.Context) error

	// Add inserts a single address into the bloom filter for a given address type.
	Add(address string, addressType enum.AddressType)

	// AddBatch inserts multiple addresses into the bloom filter for a given address type.
	AddBatch(addresses []string, addressType enum.AddressType)

	// Contains checks if a given address exists in the bloom filter for the specified type.
	Contains(address string, addressType enum.AddressType) bool

	// Clear deletes the bloom filter for a given address type.
	Clear(addressType enum.AddressType)

	// Stats returns metadata and filter info for the given address type.
	Stats(addressType enum.AddressType) map[string]any
}
