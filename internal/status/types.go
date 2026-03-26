package status

import (
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
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

// CatchupProgressSource supplies persisted catchup ranges (e.g. blockstore.Store).
type CatchupProgressSource interface {
	GetCatchupProgress(chain string) ([]blockstore.CatchupRange, error)
}

type chainState struct {
	networkID     string
	chainName     string
	internalCode  string
	networkType   string
	thresholds    config.StatusConfig
	latestBlock   uint64
	indexedBlock  uint64
	lastIndexedAt time.Time
	failedBlocks  map[uint64]struct{}
}

type chainSnapshot struct {
	networkID         string
	chainName         string
	internalCode      string
	networkType       string
	thresholds        config.StatusConfig
	latestBlock       uint64
	indexedBlock      uint64
	lastIndexedAt     time.Time
	failedBlocksCount int
}
