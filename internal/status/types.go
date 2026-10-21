package status

import (
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
	// The chain's RPC providers as the regular worker sees them, in configured order.
	Providers []ProviderStatus `json:"providers,omitempty"`
}

type StatusResponse struct {
	Timestamp time.Time       `json:"timestamp"`
	Version   string          `json:"version"`
	Networks  []NetworkStatus `json:"networks"`
}

type catchupRangeKey struct {
	start uint64
	end   uint64
}

// ProviderStatus is one RPC provider's health. Host carries no path or query: node URLs often embed API keys.
type ProviderStatus struct {
	Name              string     `json:"name"`
	Host              string     `json:"host"`
	State             string     `json:"state"`
	AvgLatencyMs      int64      `json:"avg_latency_ms"`
	ConsecutiveErrors int        `json:"consecutive_errors"`
	BlacklistedUntil  *time.Time `json:"blacklisted_until,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`
}

type chainState struct {
	networkID            string
	chainName            string
	internalCode         string
	networkType          string
	thresholds           config.StatusConfig
	latestBlock          uint64
	indexedBlock         uint64
	lastIndexedAt        time.Time
	failedBlocks         map[uint64]struct{}
	catchupRanges        map[catchupRangeKey]uint64
	catchupPendingBlocks uint64
	providers            func() []ProviderStatus
}

type chainSnapshot struct {
	networkID            string
	chainName            string
	internalCode         string
	networkType          string
	thresholds           config.StatusConfig
	latestBlock          uint64
	indexedBlock         uint64
	lastIndexedAt        time.Time
	failedBlocksCount    int
	catchupRangesCount   int
	catchupPendingBlocks uint64
	providers            func() []ProviderStatus
}
