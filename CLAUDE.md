# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Production-ready multi-chain blockchain indexer supporting EVM chains (Ethereum, BSC, Polygon, Arbitrum, Optimism), TRON, and Bitcoin. The system uses four cooperating worker types (Regular, Catchup, Rescanner, Manual) to provide reliable real-time and historical block indexing with automatic failure recovery.

## Build Commands

```bash
# Build main indexer binary
make build                    # or: go build -o indexer ./cmd/indexer

# Build all utilities
make build-all               # indexer + kv-migrate + wallet-kv-load

# Build individual utilities
make build-kv-migrate        # KV store migration tool
make build-wallet-kv-load    # Bloom filter loader

# Clean binaries
make clean
```

## Running the Indexer

```bash
# Start required infrastructure (Postgres, Redis, NATS, Consul)
docker-compose up -d

# Run with default config (all chains, regular worker only)
./indexer index

# Run specific chains with catchup worker
./indexer index --chains=ethereum_mainnet,tron_mainnet --catchup

# Enable all workers (real-time + historical + retry + manual)
./indexer index --chains=ethereum_mainnet --catchup --manual

# Start from latest block (ignore config start_block)
./indexer index --from-latest

# Debug mode with verbose logging
./indexer index --debug

# Custom config file
./indexer index --config=configs/custom.yaml
```

## Testing

```bash
# Run all tests (skips integration tests by default)
go test ./...

# Run with integration tests (requires live RPC access)
go test -short=false ./...

# Run specific package tests
go test ./pkg/ratelimiter/...
go test ./internal/rpc/evm/...

# Run with race detection
go test -race ./...

# Verbose output
go test -v ./...
```

**Test Patterns:**
- Integration tests use `testing.Short()` to skip by default (require live blockchain RPC)
- Unit tests are self-contained and run offline
- Tests use public RPC endpoints (e.g., `ethereum-rpc.publicnode.com`)

## Development Setup

1. **Prerequisites:**
   ```bash
   # Start infrastructure services
   docker-compose up -d

   # Verify services
   docker-compose ps
   # Expected: postgres_db, redis_cache, nats-server, consul all running
   ```

2. **Configuration:**
   ```bash
   # Copy example config
   cp configs/config.example.yaml configs/config.yaml

   # Edit config to add RPC endpoints and API keys
   # Chain names in config YAML must match --chains flag values
   ```

3. **Initialize Data Stores:**
   ```bash
   # Load wallet addresses into bloom filter (if using address filtering)
   make build-wallet-kv-load
   ./wallet-kv-load run --config configs/config.yaml --batch 10000

   # Verify NATS stream setup
   nats stream add transfer --subjects="transfer.event.*" --storage=file --retention=workqueue
   ```

## Architecture Overview

### Worker System

The indexer uses a multi-worker architecture where all workers extend `BaseWorker` and are managed by a central `Manager`:

- **RegularWorker**: Real-time block indexing, handles reorgs (EVM), emits transactions
- **CatchupWorker**: Backfills historical gaps in parallel, tracks progress in KV store
- **RescannerWorker**: Retries failed blocks from `failedChan` and KV store
- **ManualWorker**: Processes explicit missing block ranges from Redis ZSET (concurrent-safe)
- **MempoolWorker**: Tracks unconfirmed Bitcoin transactions (0-conf)

Workers share infrastructure: `Indexer`, `BlockStore`, `PubkeyStore`, `Emitter`, `KVStore`, `RateLimiter`

### Blockchain Abstraction

**Indexer Interface** (`internal/indexer/indexer.go`):
- Common interface: `GetLatestBlockNumber()`, `GetBlock()`, `GetBlocks()`, `IsHealthy()`
- Implementations:
  - **EVMIndexer**: Batch block fetching, receipt retrieval, reorg detection with 10-block rollback
  - **TronIndexer**: Parallel block/receipt fetching, base58 address encoding
  - **BitcoinIndexer**: UTXO-based transactions, mempool tracking, confirmation counting

**Key Types** (`pkg/common/types/types.go`):
- `Block`: Number, Hash, ParentHash, Timestamp, Transactions[]
- `Transaction`: TxHash, NetworkId, BlockNumber, FromAddress, ToAddress, Amount, TxFee, Status, Confirmations

### RPC Failover System

**Failover[T]** (`internal/rpc/failover.go`):
- Generic provider management with automatic health tracking
- Provider states: healthy -> degraded -> unhealthy -> blacklisted
- Error-based automatic blacklisting:
  - Rate limit (429): 5 min
  - Quota exceeded: 5 min
  - Timeout: 3 min
  - Connection errors: 2 min
  - Slow response (>3s): 2 min
- Round-robin selection with state awareness
- Emergency recovery when all providers unavailable

**BaseClient** (`internal/rpc/client.go`):
- Rate limiting via `PooledRateLimiter` (token bucket)
- Authentication (header or query parameter)
- Batch RPC support for efficiency
- JSON-RPC 2.0 compliance

### State Management (KV Store)

**Key Patterns:**
- `<chain>/latest_block`: Current block being indexed by RegularWorker
- `<chain>/catchup_progress/<start>-<end>`: Range processing progress
- `<chain>/failed_blocks/<block>`: Failed block metadata for retry
- `<networkType>/<address>`: Watched address registry (bloom filter)
- `missing_blocks:<chain>`: Redis ZSET of manual ranges (score = start block)
- `processing:<chain>:<start>-<end>`: Redis lock for concurrent claim
- `processed:<chain>:<start>-<end>`: Last processed block in range

**KVStore Interface** (`pkg/infra/kvstore.go`):
- Implementations: Consul, Badger (embedded)
- Methods: `Set`, `Get`, `SetAny`, `GetAny`, `List`, `Delete`
- Codec abstraction: JSON or Gob serialization

### Event Emission

**NATS JetStream** (`pkg/events/emitter.go`):
- Stream: `transfer`
- Subject: `transfer.event.dispatch`
- Idempotent publishing (transaction hash as key)
- WorkQueue retention policy
- Consumers acknowledge with explicit ACK

## Configuration Structure

**YAML Format** (`configs/config.yaml`):
```yaml
chains:
  <chain_name>:              # Must match --chains flag
    type: evm|tron|btc       # Chain type
    network_id: "1"          # Unique identifier
    internal_code: "ETH"     # For KV storage keys
    start_block: 21500000    # Initial block (override with --from-latest)
    poll_interval: "6s"      # Worker polling interval
    confirmations: 12        # Required confirmations (Bitcoin)
    throttle:
      rps: 8                 # Requests per second
      burst: 16              # Burst capacity
      batch_size: 20         # Batch request size (EVM)
    nodes:
      - url: "https://..."   # Primary RPC endpoint
        auth:
          type: header|query # Authentication method
          key: "Authorization"
          value: "Bearer ${API_KEY}"
    client:
      timeout: "20s"
      max_retries: 3
      retry_delay: "5s"

services:
  redis: { url, password }
  database: { url }           # Optional, for wallet addresses
  kvstore:
    type: consul|badger       # State persistence
    consul: { address }
    badger: { path }
  nats: { url, subject_prefix }
  bloomfilter:
    type: redis|memory
    redis_prefix: "bloom:"
  worker:
    regular: { enabled: true }
    catchup: { enabled: false, workers: 5 }
    rescanner: { enabled: true, retry_interval: "5m" }
    manual: { enabled: false }
```

## Common Development Tasks

### Adding a New Chain Type

1. **Define RPC Client** (`internal/rpc/<chain>/`):
   - Implement chain-specific RPC methods
   - Extend `BaseClient` for HTTP transport
   - Add authentication if required

2. **Implement Indexer** (`internal/indexer/<chain>.go`):
   - Implement `Indexer` interface
   - Parse blocks into `types.Block` format
   - Handle chain-specific transaction types

3. **Update Factory** (`internal/worker/factory.go`):
   - Add case in `buildIndexer()` for new chain type
   - Configure failover and rate limiting

4. **Add Network Type** (`pkg/common/enum/network.go`):
   - Define new `NetworkType` constant
   - Update validation logic

5. **Update Config** (`configs/config.example.yaml`):
   - Add example chain configuration
   - Document required RPC endpoints

### Debugging Failed Blocks

```bash
# View failed blocks in KV store (Consul)
curl http://localhost:8500/v1/kv/<chain>/failed_blocks/?keys

# Check Redis missing blocks
redis-cli ZRANGE missing_blocks:<chain> 0 -1 WITHSCORES

# Monitor NATS stream
nats stream info transfer
nats consumer sub transfer transaction-consumer

# Check indexer logs
./indexer index --debug --chains=<chain>
```

### Running Manual Backfill

```bash
# Add missing block range to Redis (from application code or redis-cli)
redis-cli ZADD missing_blocks:ethereum_mainnet 21500000 "21500000-21500100"

# Start indexer with manual worker
./indexer index --chains=ethereum_mainnet --manual

# Monitor progress
redis-cli ZRANGE missing_blocks:ethereum_mainnet 0 -1 WITHSCORES
redis-cli GET processed:ethereum_mainnet:21500000-21500100
```

### Migrating KV Store

```bash
# Edit configs/migrate.yaml with source and target KV configs
# Dry run first
make build-kv-migrate
./kv-migrate run --config configs/migrate.yaml --dry-run

# Execute migration
./kv-migrate run --config configs/migrate.yaml
```

## Code Style and Patterns

### Go Conventions
- Follow standard Go project layout (`cmd/`, `internal/`, `pkg/`)
- Idiomatic Go: accept interfaces, return structs
- Error handling: wrap with context using `fmt.Errorf("context: %w", err)`
- Concurrency: context cancellation for graceful shutdown
- Logging: structured logging with contextual fields

### Shared Patterns
- **Worker lifecycle**: `Start()` goroutine + `Stop()` context cancellation
- **Retry logic**: exponential backoff via `pkg/retry/retry.go`
- **Rate limiting**: shared per-chain limiter across all workers
- **State persistence**: save progress to KV before processing next batch
- **Failover**: automatic RPC provider switching on errors
- **Reorg handling** (EVM): maintain recent block hashes, rollback on mismatch

### Error Handling
- Chain errors -> Indexer -> Worker -> BaseWorker
- Failed blocks saved to KV: `<chain>/failed_blocks/<block>`
- Non-blocking push to `failedChan` (buffer size 100)
- RescannerWorker retries with exponential backoff
- Max retries configurable, then discard

## Key Files Reference

### Entry Points
- `cmd/indexer/main.go` - Main indexer CLI
- `cmd/kv-migrate/main.go` - KV migration utility
- `cmd/wallet-kv-load/main.go` - Bloom filter loader

### Core Logic
- `internal/worker/manager.go:174` - Worker lifecycle management
- `internal/worker/base.go:89` - Shared worker logic (rate limit, KV, emit)
- `internal/worker/regular.go:45` - Real-time block processing
- `internal/worker/catchup.go:49` - Historical backfill
- `internal/worker/rescanner.go:47` - Failed block retry
- `internal/worker/manual.go:56` - Manual block processing

### Indexers
- `internal/indexer/evm.go:35` - EVM chains (batch, receipts, reorg)
- `internal/indexer/tron.go:31` - TRON (parallel fetch, fees)
- `internal/indexer/bitcoin.go:35` - Bitcoin (UTXO, mempool)

### Infrastructure
- `internal/rpc/failover.go:55` - RPC provider failover
- `pkg/store/blockstore/store.go:26` - Block state management
- `pkg/events/emitter.go:24` - NATS event emission
- `pkg/addressbloomfilter/redis.go:24` - Address filtering
- `pkg/ratelimiter/limiter.go:15` - Rate limiting

### Configuration
- `pkg/common/config/load.go:20` - YAML config loader
- `configs/config.example.yaml` - Configuration template

## Troubleshooting

**Indexer not progressing:**
- Check RPC endpoint health: `curl <rpc_url>`
- Verify rate limits: review logs for "rate limit" or "quota exceeded"
- Check KV store connectivity: `curl http://localhost:8500/v1/status/leader` (Consul)
- Ensure NATS is running: `nats stream info transfer`

**High error rate:**
- Review provider health in logs (degraded/unhealthy/blacklisted)
- Increase retry delay or reduce RPS in config
- Check for RPC endpoint rate limits or quota
- Verify authentication headers/API keys

**Missing transactions:**
- Verify bloom filter loaded: `redis-cli EXISTS bloom:<networkType>:<address>`
- Check address format (EVM: checksummed, TRON: base58, Bitcoin: base58/bech32)
- Review logs for "address not in bloom filter" (if using filtering)
- Ensure wallet addresses loaded into KV store

**Reorg issues (EVM):**
- Increase rollback window (currently 10 blocks hardcoded in `evm.go:237`)
- Check for deep reorgs in chain logs
- RegularWorker will automatically rollback and reindex

**Redis lock contention:**
- Reduce manual worker concurrency
- Increase `split_size` to create larger ranges (fewer locks)
- Monitor Redis slow log: `redis-cli SLOWLOG GET`
