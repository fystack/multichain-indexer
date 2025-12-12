# Cardano Integration - Developer Guide

This guide is for developers who want to extend or modify Cardano support.

## Project Structure

```
internal/
├── indexer/
│   └── cardano.go          # Cardano indexer
├── rpc/
│   └── cardano/
│       ├── api.go          # CardanoAPI interface
│       ├── client.go       # Blockfrost client
│       └── types.go        # Data structures
└── worker/
    └── factory.go          # Chain factory
```

## Key Interfaces

### CardanoAPI Interface

```go
type CardanoAPI interface {
    rpc.NetworkClient
    GetLatestBlockNumber(ctx context.Context) (uint64, error)
    GetBlockByNumber(ctx context.Context, blockNumber uint64) (*Block, error)
    GetBlockHash(ctx context.Context, blockNumber uint64) (string, error)
    GetTransaction(ctx context.Context, txHash string) (*Transaction, error)
    GetBlockByHash(ctx context.Context, blockHash string) (*Block, error)
    GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]string, error)
}
```

## Adding New Features

### 1. Add New API Method

Add to CardanoAPI interface in `api.go`:

```go
GetAddressTransactions(ctx context.Context, address string) ([]Transaction, error)
```

Implement in CardanoClient in `client.go`:

```go
func (c *CardanoClient) GetAddressTransactions(
    ctx context.Context,
    address string,
) ([]Transaction, error) {
    endpoint := fmt.Sprintf("/addresses/%s/transactions", address)
    data, err := c.Do(ctx, "GET", endpoint, nil, nil)
    if err != nil {
        return nil, err
    }
    // Parse and return transactions
}
```

### 2. Testing

Create `internal/rpc/cardano/client_test.go`:

```go
func TestGetLatestBlockNumber(t *testing.T) {
    // Test implementation
}
```

### 3. Performance Tips

- Use failover for redundancy
- Configure rate limiting appropriately
- Cache frequently accessed data
- Log important operations
- Handle errors gracefully

## Code Style

1. **Naming**: Descriptive names
2. **Error Handling**: Wrap errors with context
3. **Logging**: Log operations and errors
4. **Comments**: Document public functions
5. **Testing**: Write tests for features

## Extending to Other Providers

To support alternative providers like Kupo:

1. Create: `internal/rpc/cardano/kupo/`
2. Implement CardanoAPI interface
3. Add provider selection in factory
4. Update configuration

## References

- [Blockfrost API Docs](https://docs.blockfrost.io/)
- [Cardano Developer Docs](https://developers.cardano.org/)
- [Project README](../README.md)

