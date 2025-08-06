# Transaction Indexer

A unified blockchain indexer that supports multiple chains (TRON, EVM) with real-time transaction indexing and NATS event publishing.

## Features

- **Multi-chain support**: TRON and EVM chains
- **Real-time indexing**: Continuous block processing with configurable polling
- **Event streaming**: NATS-based event publishing
- **Fault tolerance**: Automatic retry and error handling
- **Rate limiting**: Configurable rate limits per node
- **Latest block indexing**: Start from the latest block instead of a specific block number

## Quick Start

### Prerequisites

- Go 1.21+
- NATS server (optional, for event streaming)

### Installation

```bash
git clone github.com/fystasck/transaction-indexer
cd transaction-indexer
go build ./cmd/indexer
```

### Configuration

Edit `configs/config.yaml`:

```yaml
indexer:
  chains:
    tron:
      name: "tron"
      nodes:
        - "https://api.trongrid.io"
      start_block: 74399849
      is_latest: false  # Set to true to start from latest block
      batch_size: 10
      poll_interval: "3s"
      rate_limit:
        requests_per_second: 10
        burst_size: 20
      client:
        request_timeout: "10s"
        max_retries: 3
        retry_delay: "5s"
```

### Running the Indexer

#### Index from specific block (default)
```bash
./indexer index --chain tron
```

#### Index from latest block
```bash
./indexer index --chain tron --latest
```

#### Index all chains from latest blocks
```bash
./indexer index --latest
```

### Latest Block Feature

The indexer supports starting from the latest block instead of a configured start block:

1. **Command line flag**: Use `--latest` flag
   ```bash
   ./indexer index --chain tron --latest
   ```

2. **Configuration file**: Set `is_latest: true` in config
   ```yaml
   tron:
     is_latest: true  # Start from latest block
     start_block: 74399849  # Fallback if latest block fetch fails
   ```

3. **Programmatic**: Set `IsLatest` field in chain config
   ```go
   chainConfig.IsLatest = true
   ```

When `is_latest` is enabled:
- The indexer fetches the current latest block number
- Starts indexing from that block
- Falls back to `start_block` if latest block fetch fails
- Logs the starting block number for transparency

## Architecture

### Core Components

1. **Chain Interface**: Standardized blockchain interaction
2. **Node Pool**: Round-robin load balancing with health monitoring
3. **Event Emitter**: NATS-based event publishing
4. **Worker**: Individual chain processing worker
5. **Manager**: Orchestrates multiple chain workers

### Adding New Chains

1. Implement the `ChainIndexer` interface:

```go
type ChainIndexer interface {
    GetName() string
    GetLatestBlockNumber(ctx context.Context) (int64, error)
    GetBlock(ctx context.Context, number int64) (*types.Block, error)
    GetBlocks(ctx context.Context, from, to int64) ([]chains.BlockResult, error)
    IsHealthy() bool
}
```

2. Register the new chain in the manager:

```go
switch chainName {
case chains.ChainTron:
    chainIndexer = tron.NewIndexerWithConfig(chainConfig.Nodes, chainConfig)
case chains.ChainEVM:
    chainIndexer = evm.NewIndexerWithConfig(chainConfig.Nodes, chainConfig)
// Add your new chain here
}
```

## Monitoring

### Health Checks

- Node health is automatically monitored
- Failed nodes are temporarily excluded from rotation
- Automatic recovery after 30 seconds

### Metrics

Monitor the indexer through:

- NATS monitoring endpoint (local development): `http://localhost:8222`
- Application logs
- Event stream monitoring

## Performance Tuning

### Batch Size

Adjust `batch_size` based on:

- Node capacity
- Network latency
- Processing requirements

### Poll Interval

Set `poll_interval` considering:

- Block production rate
- Resource usage
- Real-time requirements

### Node Selection

Use multiple nodes for:

- High availability
- Load distribution
- Geographic proximity
