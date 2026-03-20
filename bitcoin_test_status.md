# Bitcoin Indexer – Test Status

## Test File

`internal/indexer/bitcoin_extract_test.go`

**27 unit tests** (no network) + **5 integration tests** (testnet3, skip with `-short`)

```
go test -short ./internal/indexer/ -run TestBitcoin       # unit only
go test        ./internal/indexer/ -run TestBitcoin -v    # unit + integration
```

---

## Known Bugs / Limitations

### Bug #1 — Multi-input: non-first sender invisible to `TwoWayIndexing` · **High**

`fromAddr` is always taken from `getFirstInputAddress` (input[0] only). If a monitored wallet contributes input[1+], its outgoing transfer is never emitted even with `TwoWayIndexing=true`.

- **Transfer stream:** missed
- **UTXO Spent stream:** correctly captured

### Bug #2 — Input-address-also-receives filtered as change · **Medium**

With `IndexChangeOutput=false`, any output going to an address that also appears in the input set is classified as change and silently dropped — even when that address is receiving a net payment from another party in the same tx.

- **Transfer stream:** missed
- **UTXO stream:** correctly captured

### Bug #3 — Bare multisig (P2MS): only first address captured · **Low**

`GetOutputAddress` returns `Addresses[0]` only. Participants at index 1+ in a `type=multisig` script are missed. Low severity — bare multisig is nearly extinct on mainnet; P2SH/P2WSH wrapping is standard.

### Bug #4 — Taproot (P2TR) `NormalizeBTCAddress` returns error · **Confirmed by test**

`btcutil/bech32 v1.0.2` predates BIP-350 (bech32m); `bech32.Decode` fails for `bc1p…` addresses. Transfers are **not lost** (graceful fallback to raw address), but the function incorrectly rejects valid P2TR addresses.

- **Fix:** upgrade to `btcutil/bech32` with bech32m support, or use `btcd/btcutil`

### Bug #5 — Partial prevout resolution: only `Vin[0]` checked · **Medium**

Both `convertBlockWithPrevoutResolution` and `ResolvePrevouts` skip a transaction when `Vin[0].PrevOut != nil`, without checking the rest. If `Vin[0]` is resolved but `Vin[1+]` are not, later inputs get empty `FromAddress` and contribute 0 to fee calculation.

---

## Confirmed Correct Behaviour ✓

| Scenario | Transfer stream | UTXO stream |
|---|---|---|
| Coinbase transaction | skipped | skipped |
| OP_RETURN output | skipped | skipped |
| Self-consolidation (`IndexChangeOutput=false`) | no events | correctly captured |
| `satoshisFromFloat` precision | 0.1 BTC = 10,000,000 sat (no float truncation) | — |
| Simple payment + change (`IndexChangeOutput=false`) | payment only | both UTXOs |
| Batch payment (1 input, N outputs) | N transfers | all UTXOs |

---

## Integration Test Reference

Testnet3 block **4842314** via `https://bitcoin-testnet-rpc.publicnode.com`

| Test | Tx used |
|---|---|
| Multi-input (3 inputs, 1 output) | `4d21c6ef41187b2e62cf255bd517e4ad0e736bfd0fba305bf9a16cb9e9051b21` |
| Batch payment (1 input, 3 outputs) | `d7ff8b64dee9efce1a3452dc85134e65dff541da276c724bb744bd2d3df6df21` |
| 8-input, 3-output (stress case) | `5db8748682dffdf4b827a213691b533a4e45080a7ca3c2a6d761d06478c77627` |
