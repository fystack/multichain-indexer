package config

import (
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

type Env string

const (
	DevEnv  Env = "development"
	ProdEnv Env = "production"
)

const (
	DefaultStatusHealthyMaxPendingBlocks uint64 = 50
	DefaultStatusSlowMaxPendingBlocks    uint64 = 250
)

type Config struct {
	Version     string   `yaml:"version"`
	Environment Env      `yaml:"env"      validate:"required,oneof=development production"`
	Defaults    Defaults `yaml:"defaults" validate:"required"`
	Chains      Chains   `yaml:"chains"   validate:"required,min=1"`
	Services    Services `yaml:"services" validate:"required"`
}

type Defaults struct {
	Enabled             *bool              `yaml:"enabled"`
	FromLatest          bool               `yaml:"from_latest"`
	TwoWayIndexing      bool               `yaml:"two_way_indexing"`
	PollInterval        time.Duration      `yaml:"poll_interval"         validate:"required"`
	ReorgRollbackWindow int                `yaml:"reorg_rollback_window" validate:"required,min=1"`
	Status              StatusConfig       `yaml:"status"`
	Client              ClientConfig       `yaml:"client"`
	Throttle            Throttle           `yaml:"throttle"`
	Failover            rpc.FailoverConfig `yaml:"failover"`
}

type Chains map[string]ChainConfig

type ChainConfig struct {
	Name                string           `yaml:"-"`
	Enabled             *bool            `yaml:"enabled"`
	NetworkId           string           `yaml:"network_id"`
	InternalCode        string           `yaml:"internal_code"`
	NativeDenom         string           `yaml:"native_denom"`
	Type                enum.NetworkType `yaml:"type"                  validate:"required"`
	FromLatest          bool             `yaml:"from_latest"`
	StartBlock          int              `yaml:"start_block"           validate:"min=0"`
	PollInterval        time.Duration    `yaml:"poll_interval"`
	ReorgRollbackWindow int              `yaml:"reorg_rollback_window"`
	TwoWayIndexing      bool             `yaml:"two_way_indexing"`
	Confirmations       uint64           `yaml:"confirmations"`
	MaxLag              uint64           `yaml:"max_lag"`
	IndexUTXO           bool             `yaml:"index_utxo"`
	DebugTrace          bool             `yaml:"debug_trace"`
	TraceThrottle       TraceThrottle    `yaml:"trace_throttle"`
	Status              StatusConfig     `yaml:"status"`
	Client              ClientConfig     `yaml:"client"`
	Throttle            Throttle         `yaml:"throttle"`
	Ton                 TonConfig        `yaml:"ton"`
	Nodes               []NodeConfig     `yaml:"nodes"                 validate:"required,min=1"`
}

type StatusConfig struct {
	HealthyMaxPendingBlocks uint64 `yaml:"healthy_max_pending_blocks"`
	SlowMaxPendingBlocks    uint64 `yaml:"slow_max_pending_blocks"`
}

func (s StatusConfig) Normalize() StatusConfig {
	normalized := s
	if normalized.HealthyMaxPendingBlocks == 0 {
		normalized.HealthyMaxPendingBlocks = DefaultStatusHealthyMaxPendingBlocks
	}
	if normalized.SlowMaxPendingBlocks == 0 {
		normalized.SlowMaxPendingBlocks = DefaultStatusSlowMaxPendingBlocks
	}
	return normalized
}

type ClientConfig struct {
	Timeout    time.Duration `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries" validate:"min=0"`
	RetryDelay time.Duration `yaml:"retry_delay"`
}

type Throttle struct {
	RPS         int  `yaml:"rps"`
	Burst       int  `yaml:"burst"`
	BatchSize   int  `yaml:"batch_size"`
	Concurrency int  `yaml:"concurrency"`
	Parallel    bool `yaml:"parallel"`
}

type TonConfig struct {
	// ShardScanWorkers controls parallelism at shard-range level (each worker scans
	// shard lineage sequentially to preserve ordering).
	ShardScanWorkers int `yaml:"shard_scan_workers"`
	// TxFetchWorkers controls parallel GetTransaction calls per shard block.
	TxFetchWorkers int `yaml:"tx_fetch_workers"`
}

type NodeConfig struct {
	URL        string            `yaml:"url"     validate:"required,url"`
	Auth       AuthConfig        `yaml:"auth"`
	Headers    map[string]string `yaml:"headers"`
	DebugTrace bool              `yaml:"debug_trace"` // node supports debug_* namespace
}

// TraceThrottle configures rate limiting and concurrency for debug_traceTransaction calls.
// Separate from main throttle to avoid starving block/receipt RPCs.
// Defaults: trace_rps = main rps / 2, trace_burst = main burst / 2, trace_concurrency = 4.
type TraceThrottle struct {
	RPS         int `yaml:"trace_rps"`
	Burst       int `yaml:"trace_burst"`
	Concurrency int `yaml:"trace_concurrency"`
}

type AuthConfig struct {
	Type  string `yaml:"type"  validate:"oneof=header query"`
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}
