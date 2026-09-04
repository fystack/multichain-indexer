# CLAUDE.md

Guidance for AI agents working in this repo. Keep changes small, tested, and consistent with existing patterns.

## What this is

Multi-chain blockchain transaction indexer (Go 1.25). Watches configured chains, fetches blocks, extracts transfers for monitored addresses, and emits events. Supported chains live in `internal/indexer/` (EVM, Solana, Bitcoin, Tron, Sui, Cosmos, Aptos, TON, XRP, Stellar).

## Commands

```bash
make build                                          # -> ./indexer
./indexer index --chain=<name> --catchup --debug    # run one chain (name = config key, e.g. solana_mainnet)
./indexer index --catchup                            # run all enabled chains
make stop                                            # pkill the running indexer
go test ./...                                        # all tests (some hit live RPC / need network)
go test ./pkg/adaptive/ -race                        # a single package, with race detector
go fmt ./...                                          # format before committing
```

- Config path defaults to `configs/config.yaml`. **`configs/config.yaml` is gitignored** (real keys/endpoints live there, local only). The tracked template is `configs/config.example.yaml` — update it with **placeholders** (`${HELIUS_KEY}`), never real keys.
- Running needs NATS + Redis reachable (`nats:` / `redis:` blocks in config). Without them the indexer fails at startup.

## Architecture

- `internal/indexer/` — per-chain `Indexer` implementations (`indexer.go` defines the interface: `GetBlock`, `GetBlocks`, `GetBlocksByNumbers`, `GetLatestBlockNumber`, `IsHealthy`). Each parses raw RPC blocks into `types.Transaction` transfers.
- `internal/worker/` — worker modes over an indexer, built once per chain in `factory.go` and shared across modes (`types.go`): `regular` (real-time head), `catchup` (backfill ranges), `rescanner` (retry failed blocks), `manual`, `mempool`. `base.go` holds shared block-handling/emit logic.
- `internal/rpc/` — `Failover[T]` provider pool: health tracking, blacklisting, latency-based rotation, error classification (`analyzeError`). `internal/rpc/<chain>/` has the concrete RPC clients.
- `pkg/adaptive/` — AIMD concurrency limiter used to pace RPC calls to observed latency/errors.
- `pkg/store/`, `pkg/kvstore/`, `pkg/repository/` — persistence (latest block, failed blocks, catchup ranges). `pkg/events/` — emission. `pkg/ratelimiter/` — shared per-chain RPS limiter. `pkg/common/config/` — config types.

## Conventions

- **Write minimal comments.** Prefer self-explanatory code (clear names, small functions) over comments. Only comment non-obvious rationale — a gotcha, a "why", something that would bite the next reader. Never restate what the code plainly does. No doc-comment boilerplate on every function.
- **Adding/changing a chain:** implement `indexer.Indexer` in `internal/indexer/<chain>.go`, wire an RPC client in `internal/rpc/<chain>/`, add a `build<Chain>Indexer` in `internal/worker/factory.go`, and a config block.
- **Failover tuning lives in code**, not yaml — `rpc.DefaultFailoverConfig()`. Every `NewFailover` call passes `nil` and gets those defaults. Don't reintroduce a yaml `failover:` block.
- **Solana specifics:** `getBlock` uses `encoding=json` (not `jsonParsed`) — cheaper. The parser resolves accounts by index + base58 data and must append `meta.loadedAddresses` (v0 ALT accounts: static + writable + readonly) via `solanaEffectiveAccountKeys`. Skipped slots are normal (`ErrorTypeBlockNotFound`); never treat them as failed blocks.
- **Free public RPCs cannot sustain Solana getBlock** at slot rate — expect 429s/lag without a keyed node. This is capacity, not code.
- **Errors:** classify recoverable RPC failures in `analyzeError` (rate_limit, timeout, chain_unavailable, ...) so blacklist/cooldown policy is consistent.

## Testing

- Unit tests are deterministic and offline; prefer them. Some indexer tests hit live mainnet RPC (network-dependent — a DNS/429 failure there is environmental, not your change).
- Use `-race` for anything with goroutines/locks (e.g. `pkg/adaptive`).
- After edits: `go build ./...` then run the affected package's tests.

## Git

- Branch off `main`; never commit directly to it.
- Conventional commits (`feat(scope):`, `fix(scope):`, `refactor:`, `chore:`).
- Stage only the files you changed — do not `git add -A` (unrelated WIP files may be dirty in the tree).
