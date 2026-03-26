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
			failedBlocks: make(map[uint64]struct{}),
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

func (r *Registry) Snapshot(version string, src CatchupProgressSource) StatusResponse {
	r.mu.RLock()
	states := make([]chainSnapshot, 0, len(r.chains))
	for _, state := range r.chains {
		states = append(states, chainSnapshot{
			networkID:         state.networkID,
			chainName:         state.chainName,
			internalCode:      state.internalCode,
			networkType:       state.networkType,
			thresholds:        state.thresholds,
			latestBlock:       state.latestBlock,
			indexedBlock:      state.indexedBlock,
			lastIndexedAt:     state.lastIndexedAt,
			failedBlocksCount: len(state.failedBlocks),
		})
	}
	r.mu.RUnlock()

	networks := make([]NetworkStatus, 0, len(states))
	for _, state := range states {
		headGap := uint64(0)
		if state.latestBlock > state.indexedBlock {
			headGap = state.latestBlock - state.indexedBlock
		}

		var catchupPending uint64
		catchupRanges := 0
		if src != nil && state.internalCode != "" {
			if ranges, err := src.GetCatchupProgress(state.internalCode); err == nil {
				catchupRanges = len(ranges)
				catchupPending = blockstore.CatchupPendingBlocks(ranges)
			}
		}

		pending := headGap + catchupPending

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
			CatchupPendingBlocks: catchupPending,
			CatchupRanges:        catchupRanges,
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
		networkID:    strings.ToLower(key),
		chainName:    strings.ToLower(key),
		internalCode: key,
		thresholds:   config.StatusConfig{}.Normalize(),
		failedBlocks: make(map[uint64]struct{}),
	}
	r.chains[key] = state
	return state
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
