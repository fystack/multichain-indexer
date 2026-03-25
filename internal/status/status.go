package status

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/config"
)

type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthSlow     HealthStatus = "slow"
	HealthDegraded HealthStatus = "degraded"
)

type NetworkStatus struct {
	NetworkID            string       `json:"network_id"`
	ChainName            string       `json:"chain_name"`
	InternalCode         string       `json:"internal_code"`
	NetworkType          string       `json:"network_type"`
	Health               HealthStatus `json:"health"`
	LatestBlock          uint64       `json:"latest_block"`
	IndexedBlock         uint64       `json:"indexed_block"`
	PendingBlocks        uint64       `json:"pending_blocks"`
	HeadGap              uint64       `json:"head_gap"`
	CatchupPendingBlocks uint64       `json:"catchup_pending_blocks"`
	CatchupRanges        int          `json:"catchup_ranges"`
	FailedBlocks         int          `json:"failed_blocks"`
	LastIndexedAt        *time.Time   `json:"last_indexed_at,omitempty"`
}

type StatusResponse struct {
	Timestamp time.Time       `json:"timestamp"`
	Version   string          `json:"version"`
	Networks  []NetworkStatus `json:"networks"`
}

type chainState struct {
	networkID     string
	chainName     string
	internalCode  string
	networkType   string
	thresholds    config.StatusConfig
	latestBlock   uint64
	indexedBlock  uint64
	catchupRemain uint64
	catchupRanges int
	lastIndexedAt time.Time
	failedBlocks  map[uint64]struct{}
}

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
	key := normalizeChainKey(chainKey)
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
	key := normalizeChainKey(chainKey)
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

func (r *Registry) UpdateCatchup(chainKey string, pendingBlocks uint64, ranges int) {
	key := normalizeChainKey(chainKey)
	if key == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	state.catchupRemain = pendingBlocks
	state.catchupRanges = ranges
}

func (r *Registry) MarkFailedBlock(chainKey string, blockNumber uint64) {
	key := normalizeChainKey(chainKey)
	if key == "" || blockNumber == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key)
	state.failedBlocks[blockNumber] = struct{}{}
}

func (r *Registry) ClearFailedBlocks(chainKey string, blockNumbers []uint64) {
	key := normalizeChainKey(chainKey)
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
	key := normalizeChainKey(chainKey)
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

func (r *Registry) Snapshot(version string) StatusResponse {
	r.mu.RLock()
	defer r.mu.RUnlock()

	networks := make([]NetworkStatus, 0, len(r.chains))
	for _, state := range r.chains {
		headGap := uint64(0)
		if state.latestBlock > state.indexedBlock {
			headGap = state.latestBlock - state.indexedBlock
		}
		pending := headGap + state.catchupRemain
		thresholds := state.thresholds.Normalize()

		item := NetworkStatus{
			NetworkID:            state.networkID,
			ChainName:            state.chainName,
			InternalCode:         state.internalCode,
			NetworkType:          state.networkType,
			Health:               deriveHealth(pending, thresholds),
			LatestBlock:          state.latestBlock,
			IndexedBlock:         state.indexedBlock,
			PendingBlocks:        pending,
			HeadGap:              headGap,
			CatchupPendingBlocks: state.catchupRemain,
			CatchupRanges:        state.catchupRanges,
			FailedBlocks:         len(state.failedBlocks),
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
	normalized := thresholds.Normalize()
	switch {
	case pendingBlocks < normalized.HealthyMaxPendingBlocks:
		return HealthHealthy
	case pendingBlocks < normalized.SlowMaxPendingBlocks:
		return HealthSlow
	default:
		return HealthDegraded
	}
}

func normalizeChainKey(key string) string {
	return strings.ToUpper(strings.TrimSpace(key))
}
