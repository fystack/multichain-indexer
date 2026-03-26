package status

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
)

type Registry struct {
	mu     sync.RWMutex
	chains map[string]*chainState
}

func NewRegistry() *Registry {
	return &Registry{
		chains: make(map[string]*chainState),
	}
}

func (r *Registry) RegisterChain(chainKey, chainName string, chainCfg config.ChainConfig) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, exists := r.chains[key]
	if !exists {
		state = &chainState{
			failedBlocks:  make(map[uint64]struct{}),
			catchupRanges: make(map[catchupRangeKey]uint64),
		}
		r.chains[key] = state
	}

	if strings.TrimSpace(chainCfg.NetworkId) != "" {
		state.networkID = chainCfg.NetworkId
	} else {
		state.networkID = chainName
	}
	state.chainName = chainName
	state.internalCode = chainCfg.InternalCode
	state.networkType = string(chainCfg.Type)
	state.thresholds = chainCfg.Status.Normalize()
}

func (r *Registry) UpdateHead(chainKey string, latestBlock, indexedBlock uint64, indexedAt time.Time) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	state.latestBlock = latestBlock
	state.indexedBlock = indexedBlock
	if !indexedAt.IsZero() {
		state.lastIndexedAt = indexedAt.UTC()
	}
}

func (r *Registry) MarkFailedBlock(chainKey string, blockNumber uint64) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" || blockNumber == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	state.failedBlocks[blockNumber] = struct{}{}
}

func (r *Registry) ClearFailedBlocks(chainKey string, blockNumbers []uint64) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" || len(blockNumbers) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	for _, block := range blockNumbers {
		delete(state.failedBlocks, block)
	}
}

func (r *Registry) SetFailedBlocks(chainKey string, blockNumbers []uint64) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	state.failedBlocks = make(map[uint64]struct{}, len(blockNumbers))
	for _, block := range blockNumbers {
		if block == 0 {
			continue
		}
		state.failedBlocks[block] = struct{}{}
	}
}

func (r *Registry) SetCatchupRanges(chainKey string, ranges []blockstore.CatchupRange) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	state.catchupPendingBlocks = 0
	state.catchupRanges = make(map[catchupRangeKey]uint64, len(ranges))
	for _, rng := range ranges {
		upsertCatchupRangeLocked(state, rng)
	}
}

func (r *Registry) UpsertCatchupRanges(chainKey string, ranges []blockstore.CatchupRange) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" || len(ranges) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	for _, rng := range ranges {
		upsertCatchupRangeLocked(state, rng)
	}
}

func (r *Registry) DeleteCatchupRange(chainKey string, start, end uint64) {
	key := config.CanonicalChainKey(chainKey)
	if key == "" {
		return
	}

	rangeKey, ok := makeCatchupRangeKey(start, end)
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	if pending, exists := state.catchupRanges[rangeKey]; exists {
		if state.catchupPendingBlocks >= pending {
			state.catchupPendingBlocks -= pending
		} else {
			state.catchupPendingBlocks = 0
		}
		delete(state.catchupRanges, rangeKey)
	}
}

func (r *Registry) Snapshot(version string) StatusResponse {
	r.mu.RLock()
	states := make([]chainSnapshot, 0, len(r.chains))
	for _, state := range r.chains {
		states = append(states, chainSnapshot{
			networkID:            state.networkID,
			chainName:            state.chainName,
			internalCode:         state.internalCode,
			networkType:          state.networkType,
			thresholds:           state.thresholds,
			latestBlock:          state.latestBlock,
			indexedBlock:         state.indexedBlock,
			lastIndexedAt:        state.lastIndexedAt,
			failedBlocksCount:    len(state.failedBlocks),
			catchupRangesCount:   len(state.catchupRanges),
			catchupPendingBlocks: state.catchupPendingBlocks,
		})
	}
	r.mu.RUnlock()

	networks := make([]NetworkStatus, 0, len(states))
	for _, state := range states {
		headGap := uint64(0)
		if state.latestBlock > state.indexedBlock {
			headGap = state.latestBlock - state.indexedBlock
		}
		pending := headGap + state.catchupPendingBlocks

		item := NetworkStatus{
			NetworkID:            state.networkID,
			ChainName:            state.chainName,
			InternalCode:         state.internalCode,
			NetworkType:          state.networkType,
			Health:               deriveHealth(pending, state.thresholds),
			LatestBlock:          state.latestBlock,
			IndexedBlock:         state.indexedBlock,
			PendingBlocks:        pending,
			HeadGap:              headGap,
			CatchupPendingBlocks: state.catchupPendingBlocks,
			CatchupRanges:        state.catchupRangesCount,
			FailedBlocks:         state.failedBlocksCount,
		}
		if !state.lastIndexedAt.IsZero() {
			t := state.lastIndexedAt
			item.LastIndexedAt = &t
		}
		networks = append(networks, item)
	}

	sort.Slice(networks, func(i, j int) bool {
		return networks[i].ChainName < networks[j].ChainName
	})

	return StatusResponse{
		Timestamp: time.Now().UTC(),
		Version:   version,
		Networks:  networks,
	}
}

func (r *Registry) ensureStateLocked(key string) *chainState {
	state, exists := r.chains[key]
	if exists {
		return state
	}

	state = &chainState{
		networkID:     strings.ToLower(key),
		chainName:     strings.ToLower(key),
		internalCode:  key,
		thresholds:    config.StatusConfig{}.Normalize(),
		failedBlocks:  make(map[uint64]struct{}),
		catchupRanges: make(map[catchupRangeKey]uint64),
	}
	r.chains[key] = state
	return state
}

func makeCatchupRangeKey(start, end uint64) (catchupRangeKey, bool) {
	if start == 0 || end < start {
		return catchupRangeKey{}, false
	}
	return catchupRangeKey{start: start, end: end}, true
}

func catchupPendingForRange(rng blockstore.CatchupRange) uint64 {
	if rng.End < rng.Start || rng.Start == 0 {
		return 0
	}
	if rng.Current < rng.Start {
		return rng.End - rng.Start + 1
	}
	if rng.Current < rng.End {
		return rng.End - rng.Current
	}
	return 0
}

func upsertCatchupRangeLocked(state *chainState, rng blockstore.CatchupRange) {
	rangeKey, ok := makeCatchupRangeKey(rng.Start, rng.End)
	if !ok {
		return
	}
	if state.catchupRanges == nil {
		state.catchupRanges = make(map[catchupRangeKey]uint64)
	}

	newPending := catchupPendingForRange(rng)
	oldPending := state.catchupRanges[rangeKey]
	state.catchupRanges[rangeKey] = newPending

	if state.catchupPendingBlocks >= oldPending {
		state.catchupPendingBlocks -= oldPending
	} else {
		state.catchupPendingBlocks = 0
	}
	state.catchupPendingBlocks += newPending
}

func deriveHealth(pendingBlocks uint64, thresholds config.StatusConfig) HealthStatus {
	switch {
	case pendingBlocks < thresholds.HealthyMaxPendingBlocks:
		return HealthHealthy
	case pendingBlocks < thresholds.SlowMaxPendingBlocks:
		return HealthSlow
	default:
		return HealthDegraded
	}
}
