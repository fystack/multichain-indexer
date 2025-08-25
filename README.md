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
- ⚡ **Rate Limiting** - Intelligent RPC throttling per provider
- 🔄 **Failover Support** - Multiple RPC endpoints with automatic switching
- 📊 **Real-time Streaming** - NATS-based event publishing
- 💾 **Persistent Storage** - BadgerDB with optimized block storage
- 🚀 **Concurrent Processing** - Multiple chains processed simultaneously
- 🔍 **Auto-Catchup** - Intelligent gap detection and historical processing
- 🔍 **Bloom Filter** - Redis-based address filtering for performance

### **Advanced Monitoring**
- 📈 **Comprehensive Logging** - Structured logging with slog
- 🔍 **Failed Block Management** - Dedicated recovery system
- 📊 **Performance Metrics** - Built-in status reporting
- 🔧 **Debug Mode** - Detailed operation tracing

### **Simplified CLI**
- 🎯 **Single Command** - Unified `index` command for all operations
- 🔗 **Multi-Chain Support** - Comma-separated chain names (`--chain=evm,tron`)
- 🚀 **Auto-Catchup** - Optional `--catchup` flag for gap filling
- 🐛 **Debug Mode** - `--debug` flag for verbose logging

## 📦 Installation

### Prerequisites
- **Go 1.24.5+**
- **NATS Server** (for real-time streaming)
- **Redis** (for bloom filter and caching)
- **PostgreSQL** (for transaction storage)

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
      - url: "https://tron.drpc.org"
    start_block: 75144237
    from_latest: false
    poll_interval: "4s"
    client:
      timeout: "15s"
      max_retries: 5
      retry_delay: "10s"
      throttle:
        rps: 12
        burst: 15

  evm:
    name: "ethereum-mainnet"
    nodes:
      - url: "https://ethereum-rpc.publicnode.com"
      - url: "https://1rpc.io/eth"
      # - url: "https://eth-mainnet.g.alchemy.com/v2/${API_KEY}"
        # api_key_env: "ALCHEMY_KEY"
    start_block: 23080871
    from_latest: false
    client:
      timeout: "30s"
      throttle:
        rps: 5
        burst: 10

nats:
  url: "nats://localhost:4222"
  subject_prefix: "indexer.transaction"

storage:
  type: "memory"           # memory | badger | postgres
  directory: "data/badger" # only used for badger

db:
  type: "postgres"
  url: "postgres://postgres:postgres@localhost:5432/postgres"

bloomfilter:
  backend: "redis"         # redis | in_memory
  redis:
    wallet_address_repo: "wallet_address"
    batch_size: 1000
    key_prefix: "bloomfilter"
    error_rate: 0.01
    capacity: 1000000
  in_memory:
    wallet_address_repo: "wallet_address"
    expected_items: 1000000
    false_positive_rate: 0.01
    batch_size: 1000

redis:
  url: "localhost:6379"
  password: ""
  environment: "development"
```

## 🎯 Usage

### **1. Normal Indexing (Continuous)**
Process new blocks in real-time:

```bash
# Index single chain
./indexer index --chain=evm
./indexer index --chain=tron

# Index multiple chains simultaneously
./indexer index --chain=evm,tron

# Debug mode with verbose logging
./indexer index --chain=evm --debug

# Run catchup alongside regular indexing (auto-detects gaps)
./indexer index --chain=evm --catchup
./indexer index --chain=evm,tron --catchup

# Combined: multiple chains with catchup and debug
./indexer index --chain=evm,tron --catchup --debug
```

### **2. NATS Message Monitoring**
Monitor real-time transaction events:
```bash
# Print all transactions to console
./indexer nats-printer

# Custom NATS server and subject
./indexer nats-printer --nats-url=nats://localhost:4222 --subject=indexer.transaction
```

## 🏗️ Architecture

### **Core Components**

```mermaid
flowchart TB
    subgraph "CLI Layer"
        CLI[CLI Interface<br/>Multi-chain<br/>--chain=evm,tron]
    end
    
    subgraph "Orchestration Layer"
        Manager[Manager<br/>Multi-chain Orchestrator<br/>Worker Management]
    end
    
    subgraph "Processing Layer"
        Worker[Worker<br/>Regular/Catchup Mode<br/>Per Chain]
        Indexer[Chain Indexer<br/>EVM/TRON<br/>Block Processing]
    end
    
    subgraph "Infrastructure Layer"
        RPC[RPC Failover Manager<br/>Provider Management<br/>EVM/TRON APIs]
        Emitter[Event Emitter<br/>NATS Streaming]
        BloomFilter[Bloom Filter<br/>Address Filtering<br/>Redis/In-Memory]
    end
    
    subgraph "Storage Layer"
        BlockStore[BlockStore<br/>Progress Tracking<br/>Latest Block Numbers]
        KVStore[KV Store<br/>BadgerDB<br/>Persistent Data]
        NATS[NATS Server<br/>Transaction Events<br/>Real-time Streaming]
    end
    
    %% Data Flow
    CLI --> Manager
    Manager --> Worker
    Manager --> Indexer
    Manager --> Emitter
    Manager --> BloomFilter
    
    Worker --> Indexer
    Worker --> Emitter
    Worker --> BloomFilter
    Worker --> BlockStore
    
    Indexer --> RPC
    
    BlockStore --> KVStore
    Emitter --> NATS
    
    %% Styling
    style CLI fill:#e3f2fd
    style Manager fill:#f3e5f5
    style Worker fill:#e8f5e8
    style Indexer fill:#e8f5e8
    style RPC fill:#fce4ec
    style Emitter fill:#fff3e0
    style BloomFilter fill:#fff3e0
    style BlockStore fill:#ffebee
    style KVStore fill:#ffebee
    style NATS fill:#e0f2f1
```

### **Data Flow**

1. **Multi-Chain Initialization**: Parse comma-separated chain names (`--chain=evm,tron`)
2. **Worker Creation**: Create regular + optional catchup workers per chain
3. **Gap Detection**: Auto-detect missing blocks between KV store and RPC head
4. **Concurrent Processing**: 
   - Regular workers: Process latest blocks in real-time
   - Catchup workers: Fill historical gaps in parallel
5. **Transaction Processing**: Extract and normalize transaction data
6. **Event Publishing**: Stream transactions to NATS for real-time consumption
7. **Failure Handling**: Store failed blocks for later retry
8. **State Persistence**: Track progress per chain in optimized BlockStore

### **Auto-Catchup System**

The indexer includes intelligent gap detection and catchup processing:

- **Automatic Gap Detection**: Compares KV store state with RPC head block
- **Smart Range Limiting**: Limits catchup to reasonable ranges (100k blocks max)
- **Concurrent Processing**: Catchup runs alongside regular indexing
- **Progress Persistence**: Catchup progress is saved and resumable
- **Multi-Chain Support**: Each chain has independent catchup processing

**How it works:**
1. Check latest processed block from BlockStore
2. Get current head block from RPC
3. If gap > threshold, start catchup worker
4. Catchup worker processes historical blocks in parallel
5. Regular worker continues processing latest blocks
6. Automatic deduplication prevents processing same blocks twice

### **Failed Block Recovery System**

The indexer includes a sophisticated failed block management system:

- **Automatic Retry**: Failed blocks are automatically stored with retry count
- **Intelligent Backoff**: Exponential backoff for consecutive failures
- **Status Tracking**: Monitor resolved vs unresolved failed blocks
- **Persistent Storage**: Failed blocks survive application restarts

### **Bloom Filter System**

The indexer uses bloom filters for efficient address filtering:

- **Redis-based**: Scalable bloom filter using Redis BF commands
- **In-memory fallback**: Local bloom filter for development/testing
- **Thread-safe**: Proper mutex protection for concurrent access
- **Configurable**: Adjustable error rate and capacity

### **Flow Diagram**
```mermaid
graph TB
    subgraph "Multi-Chain Indexer Architecture"
        CMD["CLI Command<br/>🚀 ./indexer index --chain=evm,tron --catchup"]
        
        subgraph "Manager Processing"
            PARSE["parseChainNames()<br/>📋 Input: 'evm,tron'<br/>📤 Output: ['evm', 'tron']"]
            
            START["Start() Method<br/>🔄 Loops through each chain<br/>✅ Creates regular workers"]
            
            CATCHUP["StartCatchupAuto() Method<br/>🔄 Loops through each chain<br/>✅ Creates catchup workers"]
        end
        
        subgraph "EVM Workers"
            EVM_REG["EVM Regular Worker<br/>📊 Mode: ModeRegular<br/>🔄 Processes: Latest EVM blocks<br/>📍 Current: 19,500,000+"]
            
            EVM_CATCH["EVM Catchup Worker<br/>📊 Mode: ModeCatchup<br/>🔄 Processes: Historical EVM blocks<br/>📍 Range: Auto-detected gap"]
        end
        
        subgraph "Tron Workers"
            TRON_REG["Tron Regular Worker<br/>📊 Mode: ModeRegular<br/>🔄 Processes: Latest Tron blocks<br/>📍 Current: 75,000,000+"]
            
            TRON_CATCH["Tron Catchup Worker<br/>📊 Mode: ModeCatchup<br/>🔄 Processes: Historical Tron blocks<br/>📍 Range: Auto-detected gap"]
        end
        
        subgraph "Shared Resources"
            KV["BadgerDB<br/>• latest_block_evm<br/>• latest_block_tron<br/>• Block storage<br/>• Progress tracking"]
            
            RPC["RPC Pools<br/>• EVM providers<br/>• Tron providers<br/>• Rate limiting<br/>• Failover"]
            
            NATS["NATS Events<br/>• indexer.transaction.evm<br/>• indexer.transaction.tron"]
            
            REDIS["Redis<br/>• Bloom filters<br/>• Address caching<br/>• Rate limiting"]
            
            DB["PostgreSQL<br/>• Transaction storage<br/>• Wallet addresses<br/>• Failed blocks"]
        end
        
        subgraph "Auto-Detection Logic"
            DETECT["For each chain:<br/>1. Get latest_block from KV<br/>2. Get latest_block from RPC<br/>3. Calculate gap<br/>4. Limit range if too large<br/>5. Start catchup if needed"]
        end
    end
    
    CMD --> PARSE
    PARSE --> START
    PARSE --> CATCHUP
    
    START --> EVM_REG
    START --> TRON_REG
    
    CATCHUP --> DETECT
    DETECT --> EVM_CATCH
    DETECT --> TRON_CATCH
    
    EVM_REG --> KV
    EVM_REG --> RPC
    EVM_REG --> NATS
    EVM_REG --> REDIS
    EVM_REG --> DB
    
    EVM_CATCH --> KV
    EVM_CATCH --> RPC
    EVM_CATCH --> NATS
    EVM_CATCH --> REDIS
    EVM_CATCH --> DB
    
    TRON_REG --> KV
    TRON_REG --> RPC
    TRON_REG --> NATS
    TRON_REG --> REDIS
    TRON_REG --> DB
    
    TRON_CATCH --> KV
    TRON_CATCH --> RPC
    TRON_CATCH --> NATS
    TRON_CATCH --> REDIS
    TRON_CATCH --> DB
    
    style CMD fill:#e8f5e8
    style EVM_REG fill:#e1f5fe
    style EVM_CATCH fill:#f3e5f5
    style TRON_REG fill:#e1f5fe
    style TRON_CATCH fill:#f3e5f5
    style DETECT fill:#fff3e0
    style REDIS fill:#ffebee
    style DB fill:#e8f5e8
```



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
├── cmd/indexer/           # CLI application (simplified single command)
├── configs/               # Configuration files
├── internal/
│   ├── indexer/          # Indexing logic
│   │   ├── manager.go    # Multi-chain orchestration + BlockStore
│   │   ├── worker.go     # Unified worker (regular/catchup modes)
│   │   ├── indexer_evm.go # Ethereum support
│   │   └── indexer_tron.go # TRON support
│   ├── rpc/              # RPC client management + failover
│   │   ├── manager.go    # Failover management
│   │   ├── client.go     # Generic RPC client
│   │   ├── evm.go        # Ethereum-specific client
│   │   └── tron.go       # TRON-specific client
│   └── events/           # NATS event streaming
├── pkg/
│   ├── addressbloomfilter/ # Bloom filter implementations
│   │   ├── redis_bloomfilter.go # Redis-based bloom filter
│   │   └── inmemory_bloomfilter.go # In-memory bloom filter
│   ├── common/           # Utilities
│   │   ├── config/       # Configuration loading
│   │   ├── logger/       # Structured logging
│   │   ├── stringutils/  # String utilities
│   │   ├── constant/     # Constants
│   │   ├── enum/         # Enumerations
│   │   └── types/        # Common types
│   ├── infra/            # Infrastructure
│   │   ├── redis.go      # Redis client
│   │   └── db.go         # Database connections
│   ├── kvstore/          # Storage abstraction
│   │   ├── kvstore.go    # Interface
│   │   ├── badger.go     # BadgerDB implementation
│   │   └── failed_block_store.go # Failed block management
│   ├── model/            # Data models
│   ├── repository/       # Repository patterns
│   ├── ratelimiter/      # Rate limiting
│   └── retry/            # Retry logic
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
go test ./pkg/kvstore -v

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

**Bloom Filter**: Configure for address filtering performance
```yaml
bloomfilter:
  backend: "redis"
  redis:
    error_rate: 0.01      # 1% false positive rate
    capacity: 1000000     # 1M addresses
    batch_size: 1000      # Batch size for operations
```

### **Memory Usage**
- **Minimal**: ~100MB base memory usage
- **Scaling**: +~20MB per active chain
- **Storage**: BadgerDB uses ~1GB per million blocks indexed
- **Redis**: ~50MB for bloom filters and caching

### **Throughput**
- **Ethereum**: ~300-800 blocks/minute (depending on RPC limits)
- **TRON**: ~600-1000 blocks/minute (with API key)
- **Multi-Chain**: Linear scaling per additional chain
- **Catchup Processing**: ~150-600 blocks/minute (historical data)
- **Failed Block Recovery**: ~50-300 blocks/minute

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
CMD ["./indexer", "index", "--chain=evm,tron", "--catchup"]
```

### **Docker Compose**
```yaml
version: '3.8'
services:
  indexer:
    build: .
    environment:
      - TRONGRID_TOKEN=${TRONGRID_TOKEN}
    depends_on:
      - nats
      - redis
      - postgres
    volumes:
      - ./data:/root/data
      - ./logs:/root/logs

  nats:
    image: nats:latest
    ports:
      - "4222:4222"

  redis:
    image: redis:alpine
    ports:
      - "6379:6379"

  postgres:
    image: postgres:15
    environment:
      POSTGRES_DB: postgres
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  postgres_data:
```

### **Systemd Service**
```ini
[Unit]
Description=Blockchain Transaction Indexer
After=network.target postgresql.service redis.service nats.service

[Service]
Type=simple
User=indexer
WorkingDirectory=/opt/indexer
Environment=TRONGRID_TOKEN=your_token_here
ExecStart=/opt/indexer/indexer index --chain=evm,tron --catchup
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
- Check Redis bloom filter health
- Verify PostgreSQL connection

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
- **Redis** - In-memory data structure store
- **PostgreSQL** - Advanced open source database

