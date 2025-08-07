# Testing Documentation

This document describes the testing strategy and test coverage for the transaction-indexer project.

## Test Structure

The project includes comprehensive tests for all major components:

### 1. KVStore Tests (`internal/kvstore/kvstore_test.go`)
- **Coverage**: 95.2%
- **Tests**: Basic operations, error handling, large values, multiple operations
- **Key Features Tested**:
  - Get/Set/Delete operations
  - Non-existent key handling
  - Empty values
  - Large values (1MB)
  - Multiple concurrent operations
  - Invalid paths
  - Store closure

### 2. Indexer Tests (`internal/indexer/worker_test.go`, `internal/indexer/manager_test.go`)
- **Coverage**: 6.0%
- **Tests**: Worker and Manager functionality
- **Key Features Tested**:
  - KVStore integration
  - Config validation
  - Block number parsing
  - Key generation
  - Error handling
  - Manager creation and validation

### 3. Config Tests (`internal/config/config_test.go`)
- **Coverage**: 77.8%
- **Tests**: Configuration loading and validation
- **Key Features Tested**:
  - YAML config loading
  - Invalid file handling
  - Invalid YAML handling
  - Chain config validation
  - Default value setting

### 4. Chain Tests (Existing)
- **EVM Coverage**: 37.2%
- **TRON Coverage**: 19.0%
- **Tests**: Chain-specific client functionality
- **Key Features Tested**:
  - Client creation
  - RPC calls
  - Error handling
  - Block retrieval
  - Batch operations

### 5. Pool Tests (Existing)
- **Coverage**: 77.5%
- **Tests**: Node pool management
- **Key Features Tested**:
  - Node switching
  - Recovery mechanisms
  - Failure handling

### 6. Rate Limiter Tests (Existing)
- **Coverage**: 81.5%
- **Tests**: Rate limiting functionality
- **Key Features Tested**:
  - Basic rate limiting
  - Try acquire functionality
  - Pooled rate limiters

## Running Tests

### Quick Test Run
```bash
# Run all tests
go test ./internal/... -v

# Run specific package tests
go test ./internal/kvstore/... -v
go test ./internal/indexer/... -v
go test ./internal/config/... -v
```

### Test Script
Use the provided test script for comprehensive testing with coverage:
```bash
./test.sh
```

### Coverage Report
```bash
# Generate coverage report
go test ./internal/... -coverprofile=coverage.out

# View coverage summary
go tool cover -func=coverage.out

# View detailed coverage in browser
go tool cover -html=coverage.out
```

## Test Categories

### Unit Tests
- **KVStore**: Tests the BadgerDB implementation
- **Config**: Tests configuration loading and validation
- **Worker**: Tests core indexing logic
- **Manager**: Tests manager creation and validation

### Integration Tests
- **KVStore Integration**: Tests saving/loading block numbers
- **Config Integration**: Tests YAML file loading
- **Error Handling**: Tests various error scenarios

### Mock Tests
- **Chain Indexers**: Mock implementations for testing
- **Event Emitters**: Mock NATS connections
- **Error Scenarios**: Simulated failure conditions

## Test Coverage Summary

| Package | Coverage | Status |
|---------|----------|--------|
| kvstore | 95.2% | ✅ Excellent |
| config | 77.8% | ✅ Good |
| ratelimiter | 81.5% | ✅ Good |
| pool | 77.5% | ✅ Good |
| evm | 37.2% | ⚠️ Needs improvement |
| tron | 19.0% | ⚠️ Needs improvement |
| indexer | 6.0% | ⚠️ Needs improvement |
| events | 0.0% | ❌ No tests |
| logger | 0.0% | ❌ No tests |
| types | 0.0% | ❌ No tests |

**Overall Coverage**: 34.5%

## Areas for Improvement

### High Priority
1. **Events Package**: Add tests for NATS event emission
2. **Logger Package**: Add tests for logging functionality
3. **Types Package**: Add tests for data structure validation

### Medium Priority
1. **Indexer Package**: Increase coverage for Worker and Manager
2. **EVM Chain**: Add more comprehensive client tests
3. **TRON Chain**: Add more comprehensive client tests

### Low Priority
1. **Integration Tests**: Add end-to-end testing
2. **Performance Tests**: Add benchmarking
3. **Stress Tests**: Add high-load testing scenarios

## Test Best Practices

### Writing New Tests
1. **Follow Naming Convention**: `TestFunctionName_Scenario`
2. **Use Descriptive Names**: Clearly describe what is being tested
3. **Test Edge Cases**: Include error conditions and boundary values
4. **Use Temporary Files**: Clean up after tests
5. **Mock External Dependencies**: Avoid real network calls in unit tests

### Test Structure
```go
func TestComponent_Scenario(t *testing.T) {
    // Setup
    tempDir, err := os.MkdirTemp("", "test")
    if err != nil {
        t.Fatalf("Failed to create temp dir: %v", err)
    }
    defer os.RemoveAll(tempDir)

    // Test logic
    // ...

    // Assertions
    if expected != actual {
        t.Errorf("Expected %v, got %v", expected, actual)
    }
}
```

### Running Tests in CI/CD
```yaml
# Example GitHub Actions step
- name: Run Tests
  run: |
    go test ./internal/... -v -coverprofile=coverage.out
    go tool cover -func=coverage.out
```

## Debugging Tests

### Verbose Output
```bash
go test -v ./internal/kvstore/...
```

### Race Detection
```bash
go test -race ./internal/...
```

### Test Timeout
```bash
go test -timeout 30s ./internal/...
```

### Specific Test
```bash
go test -run TestBadgerStore_BasicOperations ./internal/kvstore/...
```

## Continuous Integration

The test suite is designed to run in CI/CD environments:
- All tests are deterministic
- No external dependencies required for unit tests
- Coverage reporting enabled
- Fast execution (< 10 seconds for all tests)

## Contributing to Tests

When adding new features:
1. Write tests first (TDD approach)
2. Ensure coverage for new code
3. Update this documentation
4. Run the full test suite before submitting

For questions about testing, refer to the Go testing documentation or create an issue in the repository.
