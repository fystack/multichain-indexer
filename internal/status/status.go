package status

import (
	"time"

	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
)

// StatusRegistry captures status mutations used by workers.
type StatusRegistry interface {
	UpdateHead(chainKey string, latestBlock, indexedBlock uint64, indexedAt time.Time)
	MarkFailedBlock(chainKey string, blockNumber uint64)
	ClearFailedBlocks(chainKey string, blockNumbers []uint64)
	SetFailedBlocks(chainKey string, blockNumbers []uint64)
	SetCatchupRanges(chainKey string, ranges []blockstore.CatchupRange)
	UpsertCatchupRanges(chainKey string, ranges []blockstore.CatchupRange)
	DeleteCatchupRange(chainKey string, start, end uint64)
}

// NoopStatusRegistry is a no-op implementation of StatusRegistry.
type NoopStatusRegistry struct{}

func (NoopStatusRegistry) UpdateHead(string, uint64, uint64, time.Time) {}
func (NoopStatusRegistry) MarkFailedBlock(string, uint64)               {}
func (NoopStatusRegistry) ClearFailedBlocks(string, []uint64)           {}
func (NoopStatusRegistry) SetFailedBlocks(string, []uint64)             {}
func (NoopStatusRegistry) SetCatchupRanges(string, []blockstore.CatchupRange) {
}
func (NoopStatusRegistry) UpsertCatchupRanges(string, []blockstore.CatchupRange) {
}
func (NoopStatusRegistry) DeleteCatchupRange(string, uint64, uint64) {}

func EnsureStatusRegistry(statusRegistry StatusRegistry) StatusRegistry {
	if statusRegistry == nil {
		return NoopStatusRegistry{}
	}
	return statusRegistry
}
