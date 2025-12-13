# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Multi-chain blockchain transaction indexer written in Go with support for EVM chains (Ethereum, BSC, Polygon, Arbitrum, Optimism) and TRON. The system uses a four-worker architecture for real-time indexing, historical catchup, failed block rescanning, and manual block processing.

## Common Commands

### Build Commands
```bash
# Build main indexer
go build -o indexer ./cmd/indexer

# Build all binaries
make build-all

# Build individual tools
go build -o kv-migrate ./cmd/kv-migrate
go build -o wallet-kv-load ./cmd/wallet-kv-load

# Clean binaries
make clean
```

### Running the Indexer
```bash
# Start with regular workers only (real-time indexing)
./indexer index --chains=ethereum_mainnet,tron_mainnet

# Enable catchup worker (historical gap filling)
./indexer index --chains=ethereum_mainnet,tron_mainnet --catchup

# Enable manual worker (Redis-driven missing blocks)
./indexer index --chains=ethereum_mainnet,tron_mainnet --manual

# Enable rescanner worker (failed block retry)
./indexer index --chains=ethereum_mainnet,tron_mainnet --rescanner

# Enable debug logging
./indexer index --chains=ethereum_mainnet --debug

# Get help
./indexer --help
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Test specific package
go test -v ./internal/rpc/evm
go test -v ./internal/rpc/tron

# Run tests with coverage
go test -v -cover ./...
```

### Infrastructure Setup
```bash
# Start infrastructure services (NATS, Redis, Consul, PostgreSQL)
docker-compose up -d

# Stop services
docker-compose down

# Initialize bloom filter and kvstore
./wallet-kv-load run --config configs/config.yaml --batch 10000 --debug

# NATS stream management
nats stream add transfer --subjects="transfer.event.*" --storage=file --retention=workqueue
nats consumer add transfer transaction-consumer --filter="transfer.event.dispatch" --deliver=all --ack=explicit
nats consumer sub transfer transaction-consumer
nats stream info transfer
```

## Architecture Overview

### Core Components

**1. Indexer Layer** (`internal/indexer/`)
- `indexer.go` - Interface definition for all blockchain indexers
- `types.go` - Error types and BlockResult wrapper
- `evm.go` - EVM chain implementation with optimized batch processing
- `tron.go` - TRON chain implementation with parallel transaction fetching

**Key Indexer Interface:**
```go
type Indexer interface {
    GetName() string
    GetNetworkType() enum.NetworkType
    GetNetworkInternalCode() string
    GetLatestBlockNumber(ctx) (uint64, error)
    GetBlock(ctx, number) (*types.Block, error)
    GetBlocks(ctx, from, to, isParallel) ([]BlockResult, error)
    GetBlocksByNumbers(ctx, blockNumbers) ([]BlockResult, error)
    IsHealthy() bool
}
```

**2. Worker System** (`internal/worker/`)
- `base.go` - Shared worker logic (rate limiting, logging, error handling)
- `regular.go` - Real-time block indexing with reorg detection
- `catchup.go` - Historical gap filling with parallel processing
- `manual.go` - Redis ZSET-based manual block processing
- `rescanner.go` - Failed block retry with exponential backoff
- `factory.go` - Worker construction and indexer building
- `manager.go` - Lifecycle management for all workers
- `types.go` - Worker modes and event types

**3. RPC Layer** (`internal/rpc/`)
- `failover.go` - Multi-provider failover with health monitoring
- `client.go` - Base HTTP client with auth and timeout handling
- `provider.go` - Provider state tracking
- `evm/` - Ethereum JSON-RPC implementation
  - `client.go` - HTTP client with batch operations
  - `api.go` - Interface definition
  - `tx.go` - Transaction extraction (native, ERC20)
  - `types.go` - Block, Transaction, Receipt types
  - `utils.go` - EIP-55 checksum, ABI decoding
- `tron/` - TRON HTTP API implementation
  - `client.go` - HTTP client
  - `api.go` - Interface definition
  - `tx.go` - Transaction extraction (TRX, TRC10, TRC20)
  - `types.go` - Block, Transaction types
  - `address.go` - Base58 address conversion

**4. Storage** (`pkg/store/`)
- `blockstore/` - Block persistence with KV operations
  - `SaveLatestBlock(chain, blockNumber)` - Save progress
  - `GetLatestBlock(chain)` - Resume from last indexed
  - `SaveCatchupProgress(chain, start, end, current)` - Range tracking
  - `GetCatchupProgress(chain)` - Resume catchup ranges
  - `SaveFailedBlock(chain, blockNumber)` - Failed block metadata
  - `GetFailedBlocks(chain)` - Retrieve failed blocks for retry
- `pubkeystore/` - Monitored address storage with bloom filter
  - `Exist(networkType, address)` - O(1) address lookup
- `missingblockstore/` - Redis ZSET-based range tracking
  - `GetNextRange(chain)` - Claim next unprocessed range
  - `SetRangeProcessed(chain, start, end)` - Update progress
  - `RemoveRange(chain, start, end)` - Delete completed range

**5. Event System** (`pkg/events/`)
- NATS JetStream integration for publishing transaction events
- Subject: `transfer.event.dispatch`
- Stream: `transfer` with WorkQueue retention
- Only publishes transactions TO monitored addresses

**6. Infrastructure** (`pkg/infra/`)
- `kvstore.go` - KV store abstraction (Consul, BadgerDB)
- `redis.go` - Redis client with connection pooling
- `nats.go` - NATS JetStream client
- `db.go` - PostgreSQL connection

**7. Common Utilities** (`pkg/common/`)
- `types/types.go` - Standard Block and Transaction models
- `constant/constant.go` - System-wide constants
- `enum/enum.go` - Network types and enums
- `utils/` - Parsing, chunking, deduplication utilities
- `logger/` - Structured logging with slog

## Coding Conventions and Patterns

### 1. Standard Transaction Model

All chains convert to this unified model (`pkg/common/types/types.go`):

```go
type Transaction struct {
    TxHash       string          // Transaction hash
    NetworkId    string          // Chain identifier (e.g., "1" for Ethereum mainnet)
    BlockNumber  uint64          // Block number
    FromAddress  string          // Sender address (normalized)
    ToAddress    string          // Recipient address (normalized)
    AssetAddress string          // Contract address for tokens, empty for native
    Amount       string          // Amount as string (preserves precision)
    Type         string          // "transfer", "erc20_transfer", "trc10_transfer"
    TxFee        decimal.Decimal // Transaction fee in native currency
    Timestamp    uint64          // Unix timestamp
}
```

**Transaction Types** (from `pkg/common/constant/constant.go`):
- `TxnTypeTransfer` - Native currency transfer (ETH, TRX, BTC)
- `TxnTypeERC20Transfer` - ERC20 token transfer
- `TxnTypeTRC10Transfer` - TRON TRC10 token transfer

### 2. Address Normalization

**CRITICAL:** Each chain must normalize addresses to a canonical format:

**EVM Chains** - EIP-55 Checksummed addresses:
```go
// internal/rpc/evm/utils.go
func ToChecksumAddress(addr string) string {
    // Keccak256 hash, mixed case based on hash nibbles
    // Example: 0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B
}
```

**TRON** - Base58 encoded addresses:
```go
// internal/rpc/tron/address.go
func HexToTronAddress(hexAddr string) string {
    // Decodes hex to bytes, base58 encodes
    // Example: TN3W4H6rK2ce4vX9YnFQHwKENnHjoxb3m9
}
```

**Bitcoin** - Base58Check or Bech32:
```go
// For Bitcoin, implement:
func NormalizeBTCAddress(addr string) string {
    // Validate and normalize P2PKH, P2SH, or Bech32
    // Return canonical format
}
```

### 3. Transaction Extraction Pattern

Each chain implements an extraction method following this pattern:

**EVM (`internal/rpc/evm/tx.go`):**
```go
func (tx Txn) ExtractTransfers(
    network string,
    receipt *TxnReceipt,
    blockNumber, ts uint64,
) []types.Transaction {
    var out []types.Transaction
    fee := tx.calcFee(receipt)

    // 1. Extract native transfer (if value > 0 and no input data)
    if val, _ := ParseHexBigInt(tx.Value); val.Sign() > 0 && tx.To != "" {
        out = append(out, types.Transaction{...})
    }

    // 2. Extract ERC20 from logs (receipt)
    if receipt != nil {
        out = append(out, tx.parseERC20Logs(fee, network, tx.Hash, receipt.Logs, blockNumber, ts)...)
    }

    // 3. Deduplicate transfers
    return utils.DedupTransfers(out)
}
```

**TRON (`internal/rpc/tron/tx.go`):**
```go
// TRON processes at indexer level, not tx level
// See internal/indexer/tron.go:processBlock()
// 1. Parse TRC20 from logs
// 2. Parse TRX and TRC10 from top-level contracts
// 3. Assign fees to first transfer per transaction
```

**Bitcoin Pattern:**
```go
func (tx BTCTx) ExtractTransfers(
    network string,
    blockNumber, ts uint64,
    pubkeyStore PubkeyStore,
) []types.Transaction {
    var out []types.Transaction
    fee := tx.calcFee()

    // 1. Identify monitored inputs (spends FROM monitored addresses)
    // 2. Identify monitored outputs (sends TO monitored addresses)
    // 3. Create transaction for each relevant input→output pair
    // 4. Assign fee proportionally

    return out
}
```

### 4. Fee Calculation Pattern

**EVM:**
```go
func (tx Txn) calcFee(receipt *TxnReceipt) decimal.Decimal {
    // Prefer receipt data: gasUsed * effectiveGasPrice
    // Fallback to tx data: gas * gasPrice
    // Convert Wei to ETH (divide by 1e18)
    return decimal.NewFromBigInt(weiAmount, 0).Div(decimal.NewFromInt(1e18))
}
```

**TRON:**
```go
func (ti *TxnInfo) TotalFeeTRX() decimal.Decimal {
    // Fee = (fee + energy_fee + net_fee) / 1e6
    totalSun := ti.Fee + ti.Receipt.EnergyFee + ti.Receipt.NetFee
    return decimal.NewFromInt(totalSun).Div(decimal.NewFromInt(1e6))
}
```

**Bitcoin:**
```go
func (tx BTCTx) calcFee() decimal.Decimal {
    // Fee = Sum(inputs) - Sum(outputs)
    // Convert satoshis to BTC (divide by 1e8)
    feeSat := tx.TotalInput - tx.TotalOutput
    return decimal.NewFromInt(feeSat).Div(decimal.NewFromInt(1e8))
}
```

### 5. Worker Block Processing Pattern

All workers use `BaseWorker.handleBlockResult()` for consistent processing:

```go
// internal/worker/base.go
func (bw *BaseWorker) handleBlockResult(result indexer.BlockResult) bool {
    if result.Error != nil {
        // Save failed block to KV
        bw.blockStore.SaveFailedBlock(bw.chain.GetNetworkInternalCode(), result.Number)

        // Send to failedChan for RescannerWorker (non-blocking)
        select {
        case bw.failedChan <- FailedBlockEvent{...}:
        default:
            bw.logger.Warn("failedChan full")
        }
        return false
    }

    // Emit transactions TO monitored addresses
    bw.emitBlock(result.Block)
    return true
}

func (bw *BaseWorker) emitBlock(block *types.Block) {
    for _, tx := range block.Transactions {
        // Only emit if ToAddress is monitored
        if bw.pubkeyStore.Exist(bw.chain.GetNetworkType(), tx.ToAddress) {
            bw.emitter.EmitTransaction(bw.chain.GetName(), &tx)
        }
    }
}
```

### 6. Reorg Detection Pattern (EVM Only)

```go
// internal/worker/regular.go
func (rw *RegularWorker) detectAndHandleReorg(res *indexer.BlockResult) (bool, error) {
    prevNum := res.Block.Number - 1
    storedHash := rw.getBlockHash(prevNum)

    // Compare parent hash with stored hash
    if storedHash != "" && storedHash != res.Block.ParentHash {
        rollbackWindow := uint64(rw.config.ReorgRollbackWindow)
        reorgStart := prevNum - rollbackWindow

        // Clear block hash cache
        rw.clearBlockHashes()

        // Rollback to safe point
        bw.blockStore.SaveLatestBlock(chain, reorgStart-1)
        rw.currentBlock = reorgStart
        return true, nil
    }
    return false, nil
}

// Only check reorgs for EVM chains
func (rw *RegularWorker) isReorgCheckRequired() bool {
    return rw.chain.GetNetworkType() == enum.NetworkTypeEVM
}
```

### 7. KV Store Key Patterns

```go
// pkg/common/constant/constant.go
const (
    KVPrefixLatestBlock     = "latest_block"
    KVPrefixProgressCatchup = "catchup_progress"
    KVPrefixFailedBlocks    = "failed_blocks"
    KVPrefixBlockHash       = "block_hash"
)

// Keys generated:
// <chain>/latest_block
// <chain>/catchup_progress/<start>-<end>
// <chain>/failed_blocks/<block>
// <chain_type>/<address>  // for pubkey store
```

### 8. Rate Limiting Pattern

```go
// Shared rate limiter across all workers for a chain
rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
    chainName,
    chainCfg.Throttle.RPS,   // Requests per second
    chainCfg.Throttle.Burst, // Burst capacity
)
```

### 9. Failover Provider Selection

```go
// Round-robin across healthy providers
// Automatic health checks every 30s
// States: Healthy → Degraded → Blacklisted → Healthy (recovery)
// Emergency recovery: if all blacklisted, try all again
```

### 10. Transaction Deduplication

```go
// pkg/common/utils/tx.go
func DedupTransfers(in []types.Transaction) []types.Transaction {
    // Key: txHash|type|assetAddress|from|to|amount|blockNumber
    // Prevents duplicate transfers in same block (e.g., ERC20 log + input)
}
```

## Adding a New Chain Type: Bitcoin Integration Guide

This guide provides a production-grade implementation pattern for Bitcoin UTXO-based indexing.

### Overview of Bitcoin Differences

Unlike account-based chains (EVM, TRON), Bitcoin uses UTXO model:
- Transactions have multiple inputs (spending previous outputs) and outputs (creating new UTXOs)
- No "from" and "to" address pairs - must derive from input/output analysis
- Block finality through confirmations (6+ blocks recommended)
- Different reorg handling (longest chain rule, deeper reorgs possible)
- Mempool tracking for 0-conf transactions
- RBF (Replace-By-Fee) support
- Fee estimation from input/output difference

### Step 1: Create RPC Client Implementation

```
internal/rpc/btc/
├── client.go      - HTTP/RPC client
├── api.go         - Interface definition
├── tx.go          - Transaction parsing
├── types.go       - Bitcoin-specific types
├── utils.go       - Address validation, UTXO handling
└── mempool.go     - Mempool tracking
```

**Required Types (`types.go`):**

```go
type Block struct {
    Hash              string        `json:"hash"`
    Height            uint64        `json:"height"`
    PreviousBlockHash string        `json:"previousblockhash"`
    Time              uint64        `json:"time"`
    Tx                []Transaction `json:"tx"`
    Confirmations     uint64        `json:"confirmations"`
}

type Transaction struct {
    TxID     string  `json:"txid"`
    Hash     string  `json:"hash"` // Witness hash
    Version  int     `json:"version"`
    Size     int     `json:"size"`
    VSize    int     `json:"vsize"` // Virtual size (for SegWit)
    Weight   int     `json:"weight"`
    LockTime uint64  `json:"locktime"`
    Vin      []Input `json:"vin"`
    Vout     []Output `json:"vout"`
}

type Input struct {
    TxID      string   `json:"txid"`      // Previous tx hash
    Vout      uint32   `json:"vout"`      // Previous output index
    ScriptSig ScriptSig `json:"scriptSig"`
    Sequence  uint64   `json:"sequence"`
    Witness   []string `json:"txinwitness"` // For SegWit
    PrevOut   *Output  `json:"prevout"`     // Previous output (extended API)
}

type Output struct {
    Value        float64      `json:"value"` // BTC amount
    N            uint32       `json:"n"`     // Output index
    ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

type ScriptPubKey struct {
    ASM       string   `json:"asm"`
    Hex       string   `json:"hex"`
    ReqSigs   int      `json:"reqSigs"`
    Type      string   `json:"type"` // pubkeyhash, scripthash, witness_v0_keyhash, etc.
    Addresses []string `json:"addresses"`
    Address   string   `json:"address"` // Single address (extended API)
}

type MempoolEntry struct {
    TxID            string  `json:"txid"`
    VSize           int     `json:"vsize"`
    Fee             float64 `json:"fee"`
    Time            uint64  `json:"time"`
    Height          uint64  `json:"height"`
    DescendantCount int     `json:"descendantcount"`
    AncestorCount   int     `json:"ancestorcount"`
    BIP125Replaceable bool  `json:"bip125-replaceable"` // RBF flag
}
```

**API Interface (`api.go`):**

```go
type BitcoinAPI interface {
    // Block operations
    GetBlockCount(ctx context.Context) (uint64, error)
    GetBlockHash(ctx context.Context, height uint64) (string, error)
    GetBlock(ctx context.Context, hash string, verbosity int) (*Block, error)
    GetBlockByHeight(ctx context.Context, height uint64, verbosity int) (*Block, error)

    // Batch operations (critical for performance)
    BatchGetBlocksByHeight(ctx context.Context, heights []uint64) (map[uint64]*Block, error)

    // Transaction operations
    GetRawTransaction(ctx context.Context, txid string, verbose bool) (*Transaction, error)

    // Mempool operations
    GetRawMempool(ctx context.Context, verbose bool) (interface{}, error)
    GetMempoolEntry(ctx context.Context, txid string) (*MempoolEntry, error)

    // UTXO operations (for balance tracking if needed)
    GetTxOut(ctx context.Context, txid string, vout uint32) (*Output, error)

    // Network info
    GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error)
}

type BlockchainInfo struct {
    Chain         string `json:"chain"`
    Blocks        uint64 `json:"blocks"`
    Headers       uint64 `json:"headers"`
    BestBlockHash string `json:"bestblockhash"`
}
```

**Client Implementation (`client.go`):**

```go
type BitcoinClient struct {
    baseURL    string
    httpClient *http.Client
    auth       *rpc.AuthConfig
    limiter    *ratelimiter.Limiter
}

func NewBitcoinClient(
    url string,
    auth *rpc.AuthConfig,
    timeout time.Duration,
    limiter *ratelimiter.Limiter,
) *BitcoinClient {
    return &BitcoinClient{
        baseURL: url,
        httpClient: &http.Client{
            Timeout: timeout,
        },
        auth:    auth,
        limiter: limiter,
    }
}

func (c *BitcoinClient) GetBlockCount(ctx context.Context) (uint64, error) {
    var result uint64
    err := c.call(ctx, "getblockcount", []interface{}{}, &result)
    return result, err
}

func (c *BitcoinClient) GetBlockByHeight(ctx context.Context, height uint64, verbosity int) (*Block, error) {
    // First get block hash
    hash, err := c.GetBlockHash(ctx, height)
    if err != nil {
        return nil, err
    }

    // Then get block by hash
    return c.GetBlock(ctx, hash, verbosity)
}

// Batch operations for performance
func (c *BitcoinClient) BatchGetBlocksByHeight(ctx context.Context, heights []uint64) (map[uint64]*Block, error) {
    // Build batch request
    requests := make([]jsonRPCRequest, len(heights))
    for i, height := range heights {
        requests[i] = jsonRPCRequest{
            JSONRPC: "2.0",
            Method:  "getblockheader",
            Params:  []interface{}{height},
            ID:      i,
        }
    }

    // Send batch request
    // Parse responses
    // Fetch full blocks with transactions

    return blocks, nil
}

func (c *BitcoinClient) call(ctx context.Context, method string, params []interface{}, result interface{}) error {
    // Rate limiting
    if err := c.limiter.Wait(ctx); err != nil {
        return err
    }

    // Build JSON-RPC request
    reqBody := jsonRPCRequest{
        JSONRPC: "2.0",
        Method:  method,
        Params:  params,
        ID:      1,
    }

    // HTTP POST with basic auth
    // Handle response
    // Unmarshal result

    return nil
}
```

**Transaction Extraction (`tx.go`):**

```go
// ExtractTransfers extracts relevant transfers for monitored addresses
func (tx *Transaction) ExtractTransfers(
    network string,
    blockNumber, ts uint64,
    pubkeyStore PubkeyStore,
) []types.Transaction {
    var transfers []types.Transaction

    // Calculate total fee
    fee := tx.CalculateFee()

    // Analyze inputs and outputs
    // Strategy: Extract transfers where either input OR output involves monitored address

    // 1. Find all monitored input addresses (spending)
    var monitoredInputs []string
    for _, vin := range tx.Vin {
        if vin.PrevOut != nil && len(vin.PrevOut.ScriptPubKey.Addresses) > 0 {
            addr := vin.PrevOut.ScriptPubKey.Addresses[0]
            if pubkeyStore.Exist(enum.NetworkTypeBTC, addr) {
                monitoredInputs = append(monitoredInputs, addr)
            }
        }
    }

    // 2. Find all monitored output addresses (receiving)
    for _, vout := range tx.Vout {
        if len(vout.ScriptPubKey.Addresses) == 0 {
            continue // Skip unspendable outputs
        }

        outputAddr := vout.ScriptPubKey.Addresses[0]
        if !pubkeyStore.Exist(enum.NetworkTypeBTC, outputAddr) {
            continue // Skip non-monitored outputs
        }

        // Create transfer record for this output
        // FromAddress: first input address (or aggregated)
        // ToAddress: this output address
        // Amount: output value
        fromAddr := ""
        if len(tx.Vin) > 0 && tx.Vin[0].PrevOut != nil {
            if addrs := tx.Vin[0].PrevOut.ScriptPubKey.Addresses; len(addrs) > 0 {
                fromAddr = addrs[0]
            }
        }

        // Convert BTC float to satoshis string for precision
        amountSat := int64(vout.Value * 1e8)

        transfer := types.Transaction{
            TxHash:       tx.TxID,
            NetworkId:    network,
            BlockNumber:  blockNumber,
            FromAddress:  fromAddr,
            ToAddress:    outputAddr,
            AssetAddress: "", // Empty for native BTC
            Amount:       strconv.FormatInt(amountSat, 10),
            Type:         constant.TxnTypeTransfer,
            TxFee:        fee,
            Timestamp:    ts,
        }

        transfers = append(transfers, transfer)
    }

    // 3. For monitored inputs spending to non-monitored outputs (withdrawals)
    for _, inputAddr := range monitoredInputs {
        // Find where the spent funds went
        // Create transfer records showing funds leaving monitored address
        // This is optional - depends on business requirements
    }

    return transfers
}

func (tx *Transaction) CalculateFee() decimal.Decimal {
    // Fee = Sum(inputs) - Sum(outputs)
    var totalInput, totalOutput decimal.Decimal

    for _, vin := range tx.Vin {
        if vin.PrevOut != nil {
            totalInput = totalInput.Add(decimal.NewFromFloat(vin.PrevOut.Value))
        }
    }

    for _, vout := range tx.Vout {
        totalOutput = totalOutput.Add(decimal.NewFromFloat(vout.Value))
    }

    return totalInput.Sub(totalOutput)
}

// IsCoinbase checks if transaction is coinbase (block reward)
func (tx *Transaction) IsCoinbase() bool {
    return len(tx.Vin) == 1 && tx.Vin[0].TxID == ""
}

// IsRBFEnabled checks if transaction signals RBF (Replace-By-Fee)
func (tx *Transaction) IsRBFEnabled() bool {
    for _, vin := range tx.Vin {
        if vin.Sequence < 0xffffffff-1 {
            return true // BIP 125
        }
    }
    return false
}
```

**Address Utilities (`utils.go`):**

```go
// NormalizeBTCAddress validates and normalizes Bitcoin address
func NormalizeBTCAddress(addr string) (string, error) {
    // Support multiple formats:
    // - P2PKH (Legacy): 1...
    // - P2SH (Script): 3...
    // - Bech32 (SegWit): bc1...

    // Validate address format
    if strings.HasPrefix(addr, "bc1") || strings.HasPrefix(addr, "tb1") {
        // Bech32 validation
        _, _, err := bech32.Decode(addr)
        if err != nil {
            return "", fmt.Errorf("invalid bech32 address: %w", err)
        }
        return strings.ToLower(addr), nil
    }

    // Base58Check validation for legacy addresses
    decoded, err := base58.Decode(addr)
    if err != nil {
        return "", fmt.Errorf("invalid base58 address: %w", err)
    }

    if len(decoded) != 25 {
        return "", fmt.Errorf("invalid address length")
    }

    // Verify checksum
    payload := decoded[:21]
    checksum := decoded[21:]
    hash := sha256.Sum256(payload)
    hash = sha256.Sum256(hash[:])

    if !bytes.Equal(hash[:4], checksum) {
        return "", fmt.Errorf("invalid address checksum")
    }

    return addr, nil
}

// GetAddressType determines address type
func GetAddressType(addr string) string {
    if strings.HasPrefix(addr, "1") {
        return "p2pkh" // Legacy
    }
    if strings.HasPrefix(addr, "3") {
        return "p2sh" // Script
    }
    if strings.HasPrefix(addr, "bc1q") {
        return "p2wpkh" // Native SegWit
    }
    if strings.HasPrefix(addr, "bc1p") {
        return "p2tr" // Taproot
    }
    return "unknown"
}
```

**Mempool Tracking (`mempool.go`):**

```go
type MempoolTracker struct {
    client         BitcoinAPI
    trackedTxs     map[string]*MempoolEntry // txid -> entry
    monitoredAddrs map[string]bool
    mu             sync.RWMutex
}

func NewMempoolTracker(client BitcoinAPI) *MempoolTracker {
    return &MempoolTracker{
        client:     client,
        trackedTxs: make(map[string]*MempoolEntry),
    }
}

// ScanMempool scans mempool for transactions involving monitored addresses
func (mt *MempoolTracker) ScanMempool(ctx context.Context) ([]*Transaction, error) {
    // Get all mempool tx ids
    rawMempool, err := mt.client.GetRawMempool(ctx, false)
    if err != nil {
        return nil, err
    }

    txids, ok := rawMempool.([]string)
    if !ok {
        return nil, fmt.Errorf("unexpected mempool format")
    }

    var relevantTxs []*Transaction

    // Fetch details for new transactions
    for _, txid := range txids {
        mt.mu.RLock()
        _, exists := mt.trackedTxs[txid]
        mt.mu.RUnlock()

        if exists {
            continue // Already tracking
        }

        // Fetch transaction details
        tx, err := mt.client.GetRawTransaction(ctx, txid, true)
        if err != nil {
            continue // Skip on error
        }

        // Check if involves monitored address
        // If yes, add to relevantTxs and trackedTxs

        mt.mu.Lock()
        mt.trackedTxs[txid] = &MempoolEntry{
            TxID: txid,
            Time: uint64(time.Now().Unix()),
        }
        mt.mu.Unlock()

        relevantTxs = append(relevantTxs, tx)
    }

    return relevantTxs, nil
}

// CleanConfirmedTxs removes transactions that have been confirmed
func (mt *MempoolTracker) CleanConfirmedTxs(confirmedTxids []string) {
    mt.mu.Lock()
    defer mt.mu.Unlock()

    for _, txid := range confirmedTxids {
        delete(mt.trackedTxs, txid)
    }
}

// HandleRBF handles Replace-By-Fee transaction replacements
func (mt *MempoolTracker) HandleRBF(oldTxid, newTxid string) {
    mt.mu.Lock()
    defer mt.mu.Unlock()

    delete(mt.trackedTxs, oldTxid)
    // New transaction will be picked up in next scan
}
```

### Step 2: Create Bitcoin Indexer

**File: `internal/indexer/btc.go`**

```go
type BTCIndexer struct {
    chainName       string
    config          config.ChainConfig
    failover        *rpc.Failover[btc.BitcoinAPI]
    pubkeyStore     PubkeyStore
    mempoolTracker  *btc.MempoolTracker
    confirmations   uint64 // Minimum confirmations for finality
}

func NewBTCIndexer(
    chainName string,
    config config.ChainConfig,
    failover *rpc.Failover[btc.BitcoinAPI],
    pubkeyStore PubkeyStore,
) *BTCIndexer {
    confirmations := uint64(6) // Default 6 confirmations
    if config.Confirmations > 0 {
        confirmations = config.Confirmations
    }

    return &BTCIndexer{
        chainName:      chainName,
        config:         config,
        failover:       failover,
        pubkeyStore:    pubkeyStore,
        confirmations:  confirmations,
    }
}

func (b *BTCIndexer) GetName() string { return strings.ToUpper(b.chainName) }
func (b *BTCIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeBTC }
func (b *BTCIndexer) GetNetworkInternalCode() string { return b.config.InternalCode }

func (b *BTCIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
    var latest uint64
    err := b.failover.ExecuteWithRetry(ctx, func(c btc.BitcoinAPI) error {
        n, err := c.GetBlockCount(ctx)
        latest = n
        return err
    })
    return latest, err
}

func (b *BTCIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
    var btcBlock *btc.Block

    err := b.failover.ExecuteWithRetry(ctx, func(c btc.BitcoinAPI) error {
        block, err := c.GetBlockByHeight(ctx, number, 2) // Verbosity 2 = full tx details
        if err != nil {
            return err
        }
        btcBlock = block
        return nil
    })

    if err != nil {
        return nil, err
    }

    return b.convertBlock(btcBlock)
}

func (b *BTCIndexer) GetBlocks(
    ctx context.Context,
    from, to uint64,
    isParallel bool,
) ([]BlockResult, error) {
    blockNums := make([]uint64, 0, to-from+1)
    for n := from; n <= to; n++ {
        blockNums = append(blockNums, n)
    }

    return b.GetBlocksByNumbers(ctx, blockNums)
}

func (b *BTCIndexer) GetBlocksByNumbers(
    ctx context.Context,
    blockNumbers []uint64,
) ([]BlockResult, error) {
    if len(blockNumbers) == 0 {
        return nil, nil
    }

    // Fetch blocks in batches
    var blocks map[uint64]*btc.Block
    err := b.failover.ExecuteWithRetry(ctx, func(c btc.BitcoinAPI) error {
        bs, err := c.BatchGetBlocksByHeight(ctx, blockNumbers)
        blocks = bs
        return err
    })

    if err != nil {
        // Fallback to individual fetching
        return b.fetchBlocksIndividually(ctx, blockNumbers)
    }

    // Convert to standard format
    results := make([]BlockResult, 0, len(blockNumbers))
    for _, num := range blockNumbers {
        btcBlock := blocks[num]
        if btcBlock == nil {
            results = append(results, BlockResult{
                Number: num,
                Error:  &Error{ErrorType: ErrorTypeBlockNotFound, Message: "block not found"},
            })
            continue
        }

        block, err := b.convertBlock(btcBlock)
        if err != nil {
            results = append(results, BlockResult{
                Number: num,
                Error:  &Error{ErrorType: ErrorTypeBlockUnmarshal, Message: err.Error()},
            })
            continue
        }

        results = append(results, BlockResult{
            Number: num,
            Block:  block,
        })
    }

    return results, nil
}

func (b *BTCIndexer) convertBlock(btcBlock *btc.Block) (*types.Block, error) {
    var allTransfers []types.Transaction

    for _, tx := range btcBlock.Tx {
        // Skip coinbase transactions (optional)
        if tx.IsCoinbase() {
            continue
        }

        // Extract transfers for monitored addresses
        transfers := tx.ExtractTransfers(
            b.config.NetworkId,
            btcBlock.Height,
            btcBlock.Time,
            b.pubkeyStore,
        )

        allTransfers = append(allTransfers, transfers...)
    }

    return &types.Block{
        Number:       btcBlock.Height,
        Hash:         btcBlock.Hash,
        ParentHash:   btcBlock.PreviousBlockHash,
        Timestamp:    btcBlock.Time,
        Transactions: allTransfers,
    }, nil
}

func (b *BTCIndexer) IsHealthy() bool {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _, err := b.GetLatestBlockNumber(ctx)
    return err == nil
}

// Bitcoin-specific: Check if block has enough confirmations
func (b *BTCIndexer) IsConfirmed(ctx context.Context, blockNumber uint64) (bool, error) {
    latest, err := b.GetLatestBlockNumber(ctx)
    if err != nil {
        return false, err
    }

    confirmations := latest - blockNumber
    return confirmations >= b.confirmations, nil
}
```

### Step 3: Bitcoin Reorg Handling

Bitcoin reorgs are different from EVM:
- Can be deeper (not just 1-20 blocks)
- Longest chain wins (proof-of-work)
- Need to track block confirmations
- Mempool transactions may become invalid

**Create `internal/worker/regular_btc.go` or extend regular.go:**

```go
// Bitcoin-specific reorg detection
func (rw *RegularWorker) detectBTCReorg(ctx context.Context, currentBlock uint64) (bool, uint64, error) {
    // Check last N blocks for hash mismatch
    checkDepth := uint64(10) // Check last 10 blocks

    for i := uint64(1); i <= checkDepth && currentBlock >= i; i++ {
        blockNum := currentBlock - i

        // Get stored block hash
        storedHash := rw.blockStore.GetBlockHash(rw.chain.GetNetworkInternalCode(), blockNum)
        if storedHash == "" {
            continue // No stored hash
        }

        // Fetch current block hash from chain
        block, err := rw.chain.GetBlock(ctx, blockNum)
        if err != nil {
            continue
        }

        if block.Hash != storedHash {
            // Reorg detected at this depth
            logger.Warn("Bitcoin reorg detected",
                "block", blockNum,
                "expected_hash", storedHash,
                "actual_hash", block.Hash,
                "reorg_depth", i,
            )

            // Rollback to safe point (10 blocks before reorg)
            rollbackTo := blockNum - 10
            if rollbackTo < 0 {
                rollbackTo = 0
            }

            return true, rollbackTo, nil
        }
    }

    return false, 0, nil
}

// Only index confirmed blocks
func (rw *RegularWorker) processBTCBlocks() error {
    latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
    if err != nil {
        return err
    }

    // Only process blocks with enough confirmations
    btcIndexer := rw.chain.(*indexer.BTCIndexer)
    confirmedHeight := latest - btcIndexer.confirmations

    if rw.currentBlock > confirmedHeight {
        logger.Info("Waiting for confirmations",
            "current", rw.currentBlock,
            "latest", latest,
            "confirmed", confirmedHeight,
        )
        return nil
    }

    // Process up to confirmed height
    end := min(rw.currentBlock+uint64(rw.config.Throttle.BatchSize)-1, confirmedHeight)

    // Check for reorgs
    if reorg, rollbackTo, err := rw.detectBTCReorg(rw.ctx, rw.currentBlock); err == nil && reorg {
        rw.currentBlock = rollbackTo
        return nil
    }

    // Continue with normal processing
    results, err := rw.chain.GetBlocks(rw.ctx, rw.currentBlock, end, false)
    // ... handle results

    return nil
}
```

### Step 4: Update Factory and Configuration

**Update `internal/worker/factory.go`:**

```go
func buildBTCIndexer(
    chainName string,
    chainCfg config.ChainConfig,
    mode WorkerMode,
    pubkeyStore pubkeystore.Store,
) indexer.Indexer {
    failover := rpc.NewFailover[btc.BitcoinAPI](nil)

    // Shared rate limiter
    rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
        chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
    )

    for i, node := range chainCfg.Nodes {
        client := btc.NewBitcoinClient(
            node.URL,
            &rpc.AuthConfig{
                Type:  rpc.AuthType(node.Auth.Type),
                Key:   node.Auth.Key,
                Value: node.Auth.Value,
            },
            chainCfg.Client.Timeout,
            rl,
        )

        failover.AddProvider(&rpc.Provider{
            Name:       chainName + "-" + strconv.Itoa(i+1),
            URL:        node.URL,
            Network:    chainName,
            ClientType: "rpc",
            Client:     client,
            State:      rpc.StateHealthy,
        })
    }

    return indexer.NewBTCIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// In CreateManagerWithWorkers():
func CreateManagerWithWorkers(...) *Manager {
    // ...

    switch chainCfg.Type {
    case enum.NetworkTypeEVM:
        idxr = buildEVMIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
    case enum.NetworkTypeTron:
        idxr = buildTronIndexer(chainName, chainCfg, ModeRegular)
    case enum.NetworkTypeBTC:
        idxr = buildBTCIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
    default:
        logger.Fatal("Unsupported network type", "chain", chainName, "type", chainCfg.Type)
    }

    // ...
}
```

**Update `pkg/common/enum/enum.go`:**

```go
type NetworkType string

const (
    NetworkTypeEVM  NetworkType = "evm"
    NetworkTypeTron NetworkType = "tron"
    NetworkTypeBTC  NetworkType = "btc"
)
```

**Add to `configs/config.yaml`:**

```yaml
chains:
  bitcoin_mainnet:
    type: "btc"
    network_id: "bitcoin"
    internal_code: "BTC_MAINNET"
    start_block: 800000
    poll_interval: "60s" # Bitcoin blocks every ~10 minutes
    confirmations: 6      # Wait for 6 confirmations
    reorg_rollback_window: 100 # Deeper than EVM
    nodes:
      - url: "http://bitcoin-node:8332"
        auth:
          type: "basic"
          username: "bitcoinrpc"
          password: "${BTC_RPC_PASSWORD}"
      - url: "https://backup-btc-node:8332"
        auth:
          type: "basic"
          username: "bitcoinrpc"
          password: "${BTC_RPC_PASSWORD}"
    client:
      timeout: "30s"
      max_retries: 3
      retry_delay: "10s"
    throttle:
      rps: 10
      burst: 20
      batch_size: 10 # Bitcoin batches smaller than EVM
      concurrency: 2
```

### Step 5: Testing Checklist

```bash
# Unit tests
go test -v ./internal/rpc/btc/...
go test -v ./internal/indexer -run TestBTCIndexer

# Integration tests
# 1. Single block fetching
# 2. Batch block fetching
# 3. Transaction extraction for monitored addresses
# 4. Fee calculation accuracy
# 5. Reorg detection and rollback
# 6. Mempool tracking
# 7. RBF handling
# 8. Confirmation tracking
# 9. Failover between RPC nodes
# 10. Rate limiting compliance

# Manual testing
./indexer index --chains=bitcoin_mainnet --debug

# Verify NATS events
nats consumer sub transfer transaction-consumer

# Check KV store
consul kv get -recurse btc/
```

### Bitcoin-Specific Considerations

**1. Confirmation Requirements:**
- Wait for 6+ confirmations before considering block final
- Don't emit 0-conf transactions to NATS (or mark them specially)
- RegularWorker should only index confirmed blocks

**2. Reorg Handling:**
- Bitcoin reorgs can be deeper than EVM (100+ blocks possible but rare)
- Use `reorg_rollback_window: 100` in config
- Store block hashes for reorg detection
- On reorg, invalidate all blocks back to common ancestor

**3. UTXO Model:**
- No direct "from" and "to" addresses
- Must analyze inputs and outputs
- Multiple inputs → multiple outputs (complex mapping)
- For simplicity: track transfers TO monitored addresses primarily

**4. Address Types:**
- Support Legacy (P2PKH), Script (P2SH), SegWit (P2WPKH), Taproot (P2TR)
- Normalize all addresses to canonical format
- Store in pubkeyStore with all variants

**5. Fee Calculation:**
- Fee = Σ(inputs) - Σ(outputs)
- Requires fetching previous outputs (prevout)
- Use extended RPC APIs that include prevout data

**6. Mempool Handling:**
- Optional: track 0-conf transactions
- Handle RBF replacements
- Clean up confirmed transactions
- Consider fee bumping detection

**7. Performance:**
- Bitcoin blocks are large (2-4 MB)
- Limit batch size to 10-20 blocks
- Use verbosity=2 for full transaction details in one call
- Cache UTXO lookups if needed

**8. Special Transaction Types:**
- Coinbase: Skip or handle specially (miner rewards)
- OP_RETURN: Skip (data storage, not transfers)
- Multi-sig: Extract from multiple addresses
- Lightning: Not on-chain, skip

## Summary: Reusable Components

When integrating a new chain, reuse these components:

1. **Worker System** - All four workers work with any chain
2. **BaseWorker** - Block result handling, error tracking, event emission
3. **Failover System** - Multi-provider with health checks
4. **Rate Limiter** - Shared pooled rate limiting
5. **KVStore** - Progress tracking, failed blocks, catchup ranges
6. **BlockStore** - Consistent KV operations across chains
7. **PubkeyStore** - Address monitoring with bloom filter
8. **NATS Emitter** - Event publishing to JetStream
9. **Retry Logic** - Exponential backoff with configurable timeouts
10. **Configuration** - YAML-based chain configuration
11. **Logger** - Structured logging with slog
12. **Type System** - Standard Block and Transaction models

**Only implement these per chain:**
- RPC client (HTTP/gRPC communication)
- Transaction extraction logic
- Address normalization
- Fee calculation
- Block conversion to standard types
- Chain-specific reorg handling (if applicable)

**Key Success Metrics:**
- All transactions TO monitored addresses captured
- No duplicate transactions emitted
- Failed blocks retried successfully
- Reorgs handled without data loss
- Rate limits respected
- High availability through failover
