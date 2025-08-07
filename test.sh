#!/bin/bash

# Test runner script for transaction-indexer
set -e

echo "🧪 Running tests for transaction-indexer..."
echo "=========================================="

# Run all tests with coverage
echo "📊 Running tests with coverage..."
go test ./internal/... -v -cover

echo ""
echo "📈 Running coverage report..."
go test ./internal/... -coverprofile=coverage.out

# Generate coverage report
if command -v go tool cover &> /dev/null; then
    echo "📋 Coverage summary:"
    go tool cover -func=coverage.out | tail -1
    echo ""
    echo "📄 Detailed coverage report saved to coverage.out"
else
    echo "⚠️  go tool cover not available, skipping coverage report"
fi

echo ""
echo "✅ All tests completed successfully!"
