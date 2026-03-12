# Contributing to Multi-Chain Transaction Indexer

Thank you for your interest in contributing! Here are some guidelines to help you get started.

## Getting Started

1. Fork the repository and clone your fork
2. Install Go 1.24+ and required services (see [README](README.md#-prerequisites))
3. Run `go mod download` to install dependencies
4. Copy `configs/config.example.yaml` to `configs/config.yaml` and configure

## Development Workflow

1. Create a feature branch from `main`
2. Make your changes
3. Run tests: `go test ./...`
4. Run the linter: `golangci-lint run`
5. Commit with a descriptive message following [Conventional Commits](https://www.conventionalcommits.org/)
6. Open a pull request

## Commit Messages

Use the conventional commit format:

```
feat: add support for Avalanche chain
fix: handle nil pointer in TRON fee calculation
docs: update configuration examples
refactor: extract common worker logic
```

## Adding a New Chain

1. Create an indexer implementation in `internal/indexer/indexer_<chain>.go`
2. Implement the `Indexer` interface defined in `internal/indexer/indexer.go`
3. Add RPC client logic in `internal/rpc/<chain>/`
4. Register the chain type in the manager
5. Add configuration examples in `configs/config.example.yaml`
6. Add tests for the new chain indexer

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `slog` for structured logging
- Handle errors explicitly — prefer fail-fast over silent swallowing
- Write table-driven tests where applicable

## Reporting Issues

- Use GitHub Issues for bug reports and feature requests
- Include reproduction steps, expected vs actual behavior, and relevant logs
- For security vulnerabilities, email the maintainers directly instead of opening a public issue

## Code of Conduct

Please read and follow our [Code of Conduct](CODE_OF_CONDUCT.md).
