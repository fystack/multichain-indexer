# Multi-Chain Transaction Indexer

A high-performance, production-ready blockchain transaction indexer supporting multiple networks with advanced failure recovery, real-time streaming, and persistent storage.

## 🚀 Features

### **Multi-Chain Support**
- ✅ **Ethereum (EVM)** - Full support with transaction receipts
- ✅ **TRON** - Complete mainnet integration  
- 🚧 **Bitcoin, Solana** - Planned support
- 🔧 **Generic** - Extensible for custom chains

### **Production-Ready Architecture**
- 🔄 **Batch Processing** - Efficient multi-block fetching
- 🛡️ **Failure Recovery** - Persistent failed block tracking & retry
- ⚡ **Rate Limiting** - Intelligent RPC throttling
- 🔄 **Failover Support** - Multiple RPC endpoints per chain
- 📊 **Real-time Streaming** - NATS-based event publishing
- 💾 **Persistent Storage** - BadgerDB for state management

### **Advanced Monitoring**
- 📈 **Comprehensive Logging** - Structured logging with slog
- 🔍 **Failed Block Management** - Dedicated recovery system
- 📊 **Performance Metrics** - Built-in status reporting
- 🔧 **Debug Mode** - Detailed operation tracing

## 📦 Installation

### Prerequisites
- **Go 1.24+**
- **NATS Server** (for real-time streaming)

### Build from Source
```bash
git clone https://github.com/fystack/transaction-indexer.git
cd transaction-indexer
go mod download
go build -o indexer cmd/indexer/main.go
```

## ⚙️ Configuration

### Environment Variables
```bash
# Required for TRON with API key
export TRONGRID_TOKEN="your_trongrid_api_key"

# Optional for enhanced Ethereum access
export ALCHEMY_KEY="your_alchemy_key"
export INFURA_KEY="your_infura_key"
```

### Config File (`configs/config.yaml`)
```yaml
chains:
  defaults:
    batch_size: 10
    poll_interval: "5s"
    client:
      timeout: "15s"
      max_retries: 3
      retry_delay: "5s"
      throttle:
        rps: 8
        burst: 16

  tron:
    name: "tron-mainnet"
    nodes:
      - url: "https://api.trongrid.io"
        headers:
          TRON-PRO-API-KEY: "${TRONGRID_TOKEN}"
        api_key_env: "TRONGRID_TOKEN"
      - url: "https://tron-rpc.publicnode.com"
    start_block: 74399849
    poll_interval: "4s"

  evm:
    name: "ethereum-mainnet"
    nodes:
      - url: "https://ethereum-rpc.publicnode.com"
      - url: "https://1rpc.io/eth"
    start_block: 23080871

nats:
  url: "nats://localhost:4222"
  subject_prefix: "indexer.transaction"

storage:
  type: "memory"           # memory | badger
  directory: "data/badger" # for persistent storage
```

## 🎯 Usage

### **1. Normal Indexing (Continuous)**
Process new blocks in real-time:

```bash
# Index Ethereum mainnet
./indexer index --chain=evm

# Index TRON mainnet  
./indexer index --chain=tron

# Debug mode
./indexer index --chain=evm --debug
```

### **2. Failed Block Recovery**

#### One-Shot Mode (Default)
Process failed blocks once and exit:
```bash
# Process failed blocks once
./indexer index-failed --chain=tron

# With debug logging
./indexer index-failed --chain=evm --debug
```

#### Continuous Mode
Keep monitoring for failed blocks:
```bash
# Continuous failed block processing
./indexer index-failed --chain=tron --continuous
```

### **3. NATS Message Monitoring**
Monitor real-time transaction events:
```bash
# Print all transactions to console
./indexer nats-printer

# Custom NATS server and subject
./indexer nats-printer --nats-url=nats://localhost:4222 --subject=indexer.transaction
```

## 🏗️ Architecture

### **Core Components**

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   CLI Interface │───▶│     Manager      │───▶│   Worker Pool   │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │                        │
                                ▼                        ▼
                       ┌──────────────────┐    ┌─────────────────┐
                       │   Event Emitter  │    │   Chain Indexer │
                       │     (NATS)       │    │   (EVM/TRON)    │
                       └──────────────────┘    └─────────────────┘
                                │                        │
                                ▼                        ▼
                       ┌──────────────────┐    ┌─────────────────┐
                       │ Transaction Log  │    │  RPC Failover   │
                       └──────────────────┘    └─────────────────┘
                                                         │
                                                         ▼
                                                ┌─────────────────┐
                                                │ Failed Block    │
                                                │ Store (BadgerDB)│
                                                └─────────────────┘
```

### **Data Flow**

1. **Block Fetching**: Workers fetch blocks in batches from RPC endpoints
2. **Transaction Processing**: Extract and normalize transaction data
3. **Event Publishing**: Stream transactions to NATS for real-time consumption
4. **Failure Handling**: Store failed blocks for later retry
5. **State Persistence**: Track progress in BadgerDB

### **Failed Block Recovery System**

The indexer includes a sophisticated failed block management system:

- **Automatic Retry**: Failed blocks are automatically stored with retry count
- **One-Shot Recovery**: Process all failed blocks once and exit
- **Continuous Recovery**: Monitor and process failed blocks in real-time  
- **Intelligent Backoff**: Exponential backoff for consecutive failures
- **Status Tracking**: Monitor resolved vs unresolved failed blocks

## 📊 Monitoring & Logging

### **Log Levels**
```bash
# Info level (default)
./indexer index --chain=evm

# Debug level (verbose)
./indexer index --chain=evm --debug
```

### **Log Files**
- **Application logs**: Console output with structured logging
- **Failed blocks**: `logs/failed_blocks_YYYY-MM-DD.log`
- **NATS messages**: `nats.log` (when using nats-printer)

### **Status Commands**
```bash
# Check failed block status (programmatically via Manager.GetFailedBlocksStatus())
# View logs for current status
tail -f logs/failed_blocks_$(date +%Y-%m-%d).log
```

## 🔧 Development

### **Project Structure**
```
├── cmd/indexer/           # CLI application
├── configs/               # Configuration files
├── internal/
│   ├── core/             # Core types and config
│   ├── indexer/          # Indexing logic
│   │   ├── manager.go    # Orchestration
│   │   ├── worker.go     # Block processing
│   │   ├── indexer_evm.go # Ethereum support
│   │   └── indexer_tron.go # TRON support
│   ├── rpc/              # RPC client management
│   ├── events/           # NATS event streaming
│   ├── kvstore/          # Storage abstraction
│   │   ├── kvstore.go    # Interface
│   │   ├── badger.go     # BadgerDB implementation
│   │   └── failed_block_store.go # Failed block management
│   └── common/           # Utilities (rate limiting, retry)
├── logs/                 # Log files
└── data/                 # Persistent storage
```

### **Adding New Chains**

1. **Implement Indexer Interface**:
```go
type MyChainIndexer struct {
    // Implementation
}

func (m *MyChainIndexer) GetName() string { return "mychain" }
func (m *MyChainIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) { /* ... */ }
// ... implement other methods
```

2. **Register in Manager**:
```go
case rpc.NetworkMyChain:
    idx, err := NewMyChainIndexer(chainConfig)
    // ...
```

3. **Add Configuration**:
```yaml
chains:
  mychain:
    name: "mychain-mainnet"
    nodes:
      - url: "https://api.mychain.com"
    start_block: 1000000
```

### **Testing**
```bash
# Run all tests
go test ./...

# Test specific package
go test ./internal/kvstore -v

# Test with coverage
go test -cover ./...
```

## 🚦 Performance & Scaling

### **Tuning Parameters**

**Batch Size**: Adjust based on RPC limits and memory
```yaml
batch_size: 10  # Process 10 blocks per request
```

**Poll Interval**: Balance between real-time and rate limits
```yaml
poll_interval: "5s"  # Check for new blocks every 5 seconds
```

**Rate Limiting**: Respect RPC provider limits
```yaml
throttle:
  rps: 8    # 8 requests per second
  burst: 16 # Allow bursts up to 16
```

### **Memory Usage**
- **Minimal**: ~50MB base memory usage
- **Scaling**: +~10MB per active chain
- **Storage**: BadgerDB uses ~1GB per million blocks indexed

### **Throughput**
- **Ethereum**: ~500-1000 blocks/minute (depending on RPC limits)
- **TRON**: ~800-1200 blocks/minute (with API key)
- **Failed Block Recovery**: ~100-500 blocks/minute

## 🛠️ Production Deployment

### **Docker Deployment**
```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o indexer cmd/indexer/main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/indexer .
COPY configs/ configs/
CMD ["./indexer", "index", "--chain=evm"]
```

### **Systemd Service**
```ini
[Unit]
Description=Blockchain Transaction Indexer
After=network.target

[Service]
Type=simple
User=indexer
WorkingDirectory=/opt/indexer
ExecStart=/opt/indexer/indexer index --chain=evm
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### **Health Checks**
- Monitor log files for errors
- Check NATS connectivity
- Verify block progression
- Monitor failed block count

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🆘 Support

- **Issues**: [GitHub Issues](https://github.com/fystack/transaction-indexer/issues)
- **Discussions**: [GitHub Discussions](https://github.com/fystack/transaction-indexer/discussions)
- **Documentation**: [Wiki](https://github.com/fystack/transaction-indexer/wiki)

## 🙏 Acknowledgments

- **BadgerDB** - High-performance key-value store
- **NATS** - Real-time messaging system
- **Kong** - Command-line argument parsing
- **slog** - Structured logging

