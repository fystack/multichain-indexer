# Cardano Integration Guide

This document provides comprehensive information about integrating Cardano into the multichain-indexer.

## Overview

Cardano is now fully integrated into the multichain-indexer as a supported blockchain network. The integration uses the **Blockfrost API** (https://blockfrost.io/) for accessing Cardano mainnet and testnet data.

## Architecture

### Cardano Indexer Components

1. **CardanoAPI Interface** (`internal/rpc/cardano/api.go`)
   - Defines the contract for Cardano RPC operations
   - Methods: `GetLatestBlockNumber`, `GetBlockByNumber`, `GetTransaction`, etc.

2. **CardanoClient** (`internal/rpc/cardano/client.go`)
   - Implements the CardanoAPI interface
   - Uses REST API (Blockfrost) instead of JSON-RPC
   - Handles authentication via API keys
   - Supports rate limiting and failover

3. **CardanoIndexer** (`internal/indexer/cardano.go`)
   - Implements the Indexer interface
   - Converts Cardano blocks to the common Block type
   - Handles UTXO model transactions
   - Processes inputs and outputs

## Setup Instructions

### 1. Get Blockfrost API Key

1. Visit https://blockfrost.io/
2. Sign up for a free account
3. Create a new project for Cardano mainnet
4. Copy your API key (project_id)

### 2. Configure Cardano Chain

Update `configs/config.yaml`:

```yaml
chains:
  cardano_mainnet:
    internal_code: "CARDANO_MAINNET"
    network_id: "cardano"
    type: "cardano"
    start_block: 10000000  # Starting block height
    poll_interval: "10s"   # Cardano block time ~20s
    nodes:
      - url: "https://cardano-mainnet.blockfrost.io/api/v0"
        auth:
          type: "header"
          key: "project_id"
          value: "${BLOCKFROST_API_KEY}"
    client:
      timeout: "30s"
      max_retries: 3
      retry_delay: "5s"
    throttle:
      rps: 10      # Blockfrost free tier: 10 req/s
      burst: 20
```

### 3. Set Environment Variables

```bash
export BLOCKFROST_API_KEY="your_api_key_here"
```

### 4. Run the Indexer

```bash
# Index Cardano mainnet
./indexer index --chains=cardano_mainnet

# Index Cardano with catchup
./indexer index --chains=cardano_mainnet --catchup

# Index multiple chains
./indexer index --chains=ethereum_mainnet,cardano_mainnet,tron_mainnet
```

## Transaction Model

Cardano uses the **UTXO (Unspent Transaction Output)** model, different from Ethereum's account model.

### Transaction Structure

```go
type Transaction struct {
    Hash     string    // Transaction hash
    Slot     uint64    // Slot number
    BlockNum uint64    // Block height
    Inputs   []Input   // UTXOs being spent
    Outputs  []Output  // New UTXOs created
    Fee      uint64    // Transaction fee in lovelace
}

type Input struct {
    Address string // Source address
    Amount  uint64 // Amount in lovelace
    TxHash  string // Previous tx hash
    Index   uint32 // Output index
}

type Output struct {
    Address string // Destination address
    Amount  uint64 // Amount in lovelace
    Index   uint32 // Output index
}
```

### Conversion to Common Format

The CardanoIndexer converts Cardano transactions to the common Transaction format:

- **FromAddress**: First input address (sender)
- **ToAddress**: Output address (recipient)
- **Amount**: Output amount in lovelace (1 ADA = 1,000,000 lovelace)
- **Type**: "transfer"
- **TxFee**: Transaction fee in lovelace

## API Endpoints Used

The integration uses the following Blockfrost API endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /blocks/latest` | Get latest block |
| `GET /blocks/{height}` | Get block by height |
| `GET /blocks/{hash}` | Get block by hash |
| `GET /blocks/{height}/txs` | Get transaction hashes in block |
| `GET /txs/{hash}` | Get transaction details |

## Rate Limiting

Blockfrost API rate limits:
- **Free tier**: 10 requests/second
- **Paid tier**: Up to 500 requests/second

Configure throttling in `config.yaml`:

```yaml
throttle:
  rps: 10      # Requests per second
  burst: 20    # Burst capacity
```

## Monitoring and Health Checks

### Health Check

The indexer includes health checks for Cardano:

```bash
# Health check endpoint
curl http://localhost:8080/health
```

### Logging

Enable debug logging to see Cardano operations:

```bash
./indexer index --chains=cardano_mainnet --debug
```

## Troubleshooting

### Common Issues

1. **API Key Invalid**
   ```
   Error: RPC error: Invalid project_id
   ```
   - Verify your Blockfrost API key
   - Check environment variable is set correctly

2. **Rate Limit Exceeded**
   ```
   Error: RPC error: Rate limit exceeded
   ```
   - Reduce `rps` and `burst` in config
   - Consider upgrading Blockfrost plan

3. **Block Not Found**
   ```
   Error: failed to get block: block not found
   ```
   - Verify block height exists on Cardano
   - Check network (mainnet vs testnet)

4. **Connection Timeout**
   ```
   Error: context deadline exceeded
   ```
   - Increase `client.timeout` in config
   - Check internet connection
   - Verify Blockfrost API is accessible

## Advanced Configuration

### Multiple Providers (Failover)

Configure multiple Blockfrost projects for redundancy:

```yaml
chains:
  cardano_mainnet:
    type: "cardano"
    nodes:
      - url: "https://cardano-mainnet.blockfrost.io/api/v0"
        auth:
          type: "header"
          key: "project_id"
          value: "${BLOCKFROST_API_KEY_1}"
      - url: "https://cardano-mainnet.blockfrost.io/api/v0"
        auth:
          type: "header"
          key: "project_id"
          value: "${BLOCKFROST_API_KEY_2}"
```

### Testnet Configuration

For Cardano testnet:

```yaml
chains:
  cardano_testnet:
    internal_code: "CARDANO_TESTNET"
    network_id: "cardano_testnet"
    type: "cardano"
    start_block: 1000000
    nodes:
      - url: "https://cardano-testnet.blockfrost.io/api/v0"
        auth:
          type: "header"
          key: "project_id"
          value: "${BLOCKFROST_TESTNET_KEY}"
```

## Performance Considerations

1. **Block Fetching**: Sequential (no batch API available)
2. **Transaction Processing**: Parallel within a block
3. **Memory Usage**: Moderate (UTXO model is lighter than EVM)
4. **API Calls per Block**: ~2-3 calls (block info + transactions)

## Future Enhancements

- [ ] Support for Kupo indexer (alternative to Blockfrost)
- [ ] Native Cardano node support (via Ogmios)
- [ ] Smart contract event indexing
- [ ] Token metadata resolution
- [ ] Staking pool monitoring

## References

- [Blockfrost API Documentation](https://docs.blockfrost.io/)
- [Cardano Documentation](https://docs.cardano.org/)
- [UTXO Model Explanation](https://docs.cardano.org/learn/eutxo)
- [Lovelace Unit](https://docs.cardano.org/learn/cardano-addresses)

## Support

For issues or questions about Cardano integration:
1. Check the troubleshooting section above
2. Review Blockfrost API documentation
3. Open an issue on the project repository

