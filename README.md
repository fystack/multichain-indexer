# Multi-Chain Blockchain Indexer

A modular, high-performance blockchain indexer supporting multiple chains with round-robin node selection, NATS event emission, and clean architecture.

## Features

- **Multi-Chain Support**: Extensible architecture for multiple blockchains
- **TRON Implementation**: Full TRON blockchain indexing support
- **Round-Robin Load Balancing**: Automatic failover between RPC nodes
- **Event-Driven Architecture**: Real-time events via NATS
- **Modular Design**: Easy to extend with new chains and storage backends
- **Health Monitoring**: Automatic node health detection and recovery

## Quick Start

### Prerequisites

- Go 1.21+
- NATS Server
- Docker (optional)

### Installation

1. Clone the repository:
```bash
git clone <repository-url>
cd blockchain-indexer
```

2. Install dependencies:
```bash
go mod download
```

3. Start NATS server:
```bash
# Using Docker
docker run -p 4222:4222 -p 8222:8222 nats:latest --http_port 8222 --js

# Or install locally
# https://docs.nats.io/running-a-nats-service/introduction/installation
```

4. Configure the indexer:
```bash
cp configs/config.yaml.example configs/config.yaml
# Edit configs/config.yaml with your settings
```

5. Run the indexer:
```bash
go run cmd/indexer/main.go
```

### Docker Setup

```bash
# Build and run with Docker Compose
docker-compose up --build
```

## Configuration

### Chain Configuration

```yaml
indexer:
  chains:
    tron:
      name: "tron"
      nodes:
        - "https://api.trongrid.io"
        - "https://api.tronstack.io"
      start_block: 0      # Starting block number
      batch_size: 10      # Blocks per batch
      poll_interval: "3s" # Polling interval
```

### NATS Configuration

```yaml
  nats:
    url: "nats://localhost:4222"
    subject_prefix: "blockchain.indexer"
```

## Event Types

The indexer emits the following event types via NATS:

### Block Events
- **Subject**: `blockchain.indexer.{chain}.block.indexed`
- **Data**: Complete block information with transactions

### Transaction Events
- **Subject**: `blockchain.indexer.{chain}.transaction.indexed`
- **Data**: Individual transaction details

### Error Events
- **Subject**: `blockchain.indexer.{chain}.indexer.error`
- **Data**: Error information and context

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
    GetLatestBlockNumber() (int64, error)
    GetBlock(number int64) (*types.Block, error)
    GetBlocks(from, to int64) ([]*types.Block, error)
    IsHealthy() bool
}
```

2. Register the new chain in the manager:
```go
switch chainName {
case "tron":
    chainIndexer = tron.NewIndexer(chainConfig.Nodes)
case "ethereum":
    chainIndexer = ethereum.NewIndexer(chainConfig.Nodes)
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
- NATS monitoring endpoint: `http://localhost:8222`
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
- Load distribution
- High availability
- Rate limit avoidance

## Development

### Project Structure
```
blockchain-indexer/
├── cmd/indexer/           # Application entry point
├── internal/
│   ├── config/           # Configuration management
│   ├── types/            # Common types
│   ├── nodepool/         # Round-robin node pool
│   ├── events/           # NATS event emitter
│   ├── chains/           # Chain implementations
│   └── indexer/          # Core indexing logic
└── configs/              # Configuration files
```

### Testing
```bash
# Run tests
go test ./...

# Run with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Implement your changes
4. Add tests
5. Submit a pull request