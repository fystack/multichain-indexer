# Cardano Integration - Quick Start

Get Cardano indexing running in 5 minutes!

## Prerequisites

- Go 1.24.5 or later
- Docker & Docker Compose (for services)
- Blockfrost API key (free at https://blockfrost.io/)

## Step 1: Get Blockfrost API Key

1. Visit https://blockfrost.io/
2. Sign up for free
3. Create a Cardano mainnet project
4. Copy your API key

## Step 2: Configure Environment

```bash
# Set your API key
export BLOCKFROST_API_KEY="your_api_key_here"
```

## Step 3: Update Config

Edit `configs/config.yaml` and add:

```yaml
chains:
  cardano_mainnet:
    internal_code: "CARDANO_MAINNET"
    network_id: "cardano"
    type: "cardano"
    start_block: 10000000
    poll_interval: "10s"
    nodes:
      - url: "https://cardano-mainnet.blockfrost.io/api/v0"
        auth:
          type: "header"
          key: "project_id"
          value: "${BLOCKFROST_API_KEY}"
    client:
      timeout: "30s"
      max_retries: 3
    throttle:
      rps: 10
      burst: 20
```

## Step 4: Start Services

```bash
# Start NATS, Redis, Consul, PostgreSQL
docker-compose up -d
```

## Step 5: Run Indexer

```bash
# Build
go build -o indexer cmd/indexer/main.go

# Index Cardano
./indexer index --chains=cardano_mainnet

# With catchup for historical blocks
./indexer index --chains=cardano_mainnet --catchup

# Multiple chains
./indexer index --chains=ethereum_mainnet,cardano_mainnet,tron_mainnet
```

## Step 6: Verify

```bash
# Check health
curl http://localhost:8080/health

# View logs
docker-compose logs -f
```

## What's Happening?

1. **Block Fetching**: Indexer fetches Cardano blocks from Blockfrost
2. **Transaction Processing**: Extracts UTXOs and converts to standard format
3. **Event Publishing**: Publishes transactions to NATS JetStream
4. **State Persistence**: Saves progress to KV store (Consul/Badger)

## Common Commands

```bash
# Real-time indexing only
./indexer index --chains=cardano_mainnet

# With historical catchup
./indexer index --chains=cardano_mainnet --catchup

# With manual block processing
./indexer index --chains=cardano_mainnet --manual

# Debug mode
./indexer index --chains=cardano_mainnet --debug

# Multiple chains
./indexer index --chains=cardano_mainnet,ethereum_mainnet
```

## Consume Events

```bash
# Using NATS CLI
nats consumer sub transfer my-consumer

# Using Go
# See README.md for example code
```

## Troubleshooting

**API Key Error?**
```bash
# Verify key is set
echo $BLOCKFROST_API_KEY

# Check config has correct key
grep -A5 cardano_mainnet configs/config.yaml
```

**Rate Limited?**
```yaml
# Reduce in config.yaml
throttle:
  rps: 5      # Lower from 10
  burst: 10   # Lower from 20
```

**Block Not Found?**
```bash
# Check Cardano mainnet is working
curl -H "project_id: $BLOCKFROST_API_KEY" \
  https://cardano-mainnet.blockfrost.io/api/v0/blocks/latest
```

## Next Steps

- Read [CARDANO_INTEGRATION.md](./CARDANO_INTEGRATION.md) for detailed docs
- Configure multiple providers for failover
- Set up monitoring and alerting
- Integrate with your application

## Support

- Blockfrost Docs: https://docs.blockfrost.io/
- Cardano Docs: https://docs.cardano.org/
- Project Issues: GitHub issues

