package worker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/internal/status"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const (
	defaultSolanaFullCheckInterval = 10 * time.Minute
	solanaAddressPageSize          = 1000
	solanaRecentSignatures         = 10000
	// A token_mints entry ending in this is a Token-2022 mint; others are classic SPL Token mints.
	solanaToken2022Suffix = ":token-2022"
)

// solanaAccountSource is what SolanaAccountWorker needs from the chain (implemented by *indexer.SolanaIndexer).
type solanaAccountSource interface {
	AccountStates(ctx context.Context, keys []string) (uint64, []string, error)
	NewSignatures(ctx context.Context, address, until string, floor uint64) ([]solana.SignatureInfo, error)
	TransactionBlock(ctx context.Context, signature string) (*types.Block, error)
	TokenAccounts(ctx context.Context, owner string) ([]string, error)
}

// cursorStore persists each account's progress (infra.KVStore satisfies it).
type cursorStore interface {
	GetAny(k string, v any) (bool, error)
	SetAny(k string, v any) error
}

// SolanaAccountWorker indexes a Solana chain by watching accounts instead of reading every slot (watch_mode:
// accounts). Each poll it reads the balances of every watched account, the addresses in wallet_addresses plus their
// token accounts, in calls of 100, and for each account whose balance changed fetches the transactions it hasn't
// processed yet and emits their monitored transfers. Every FullCheckInterval it checks every account's history anyway,
// so a missed change can't lose a deposit.
//
// Each account's newest processed signature is kept in the KV store, so a restart misses nothing. On its first start
// the worker only takes transactions after a floor slot: where block mode left off, or the chain head. Older ones were
// indexed by block mode or predate the switch.
type SolanaAccountWorker struct {
	*BaseWorker
	source            solanaAccountSource
	loader            AddressLoader
	cursors           cursorStore
	mints             []solanaMint
	fullCheckInterval time.Duration
	now               func() time.Time

	floor        uint64
	floorLoaded  bool
	loadedAfter  time.Time
	owners       []string
	accounts     []string
	watched      map[string]bool
	fingerprints map[string]string
	dirty        map[string]bool
	cursorCache  map[string]string
	lastFull     time.Time
	recent       *recentSignatures
}

type solanaMint struct {
	mint    string
	program string
}

func NewSolanaAccountWorker(
	ctx context.Context,
	chain *indexer.SolanaIndexer,
	cfg config.ChainConfig,
	kv infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	loader AddressLoader,
	failedChan chan FailedBlockEvent,
	statusRegistry status.StatusRegistry,
) *SolanaAccountWorker {
	base := newWorkerWithMode(ctx, chain, cfg, kv, blockStore, emitter, pubkeyStore, ModeRegular, failedChan, statusRegistry)
	return newSolanaAccountWorker(base, chain, loader, kv, cfg.Solana)
}

func newSolanaAccountWorker(
	base *BaseWorker,
	source solanaAccountSource,
	loader AddressLoader,
	cursors cursorStore,
	cfg config.SolanaConfig,
) *SolanaAccountWorker {
	interval := cfg.FullCheckInterval
	if interval <= 0 {
		interval = defaultSolanaFullCheckInterval
	}
	mints := make([]solanaMint, 0, len(cfg.TokenMints))
	for _, m := range cfg.TokenMints {
		if mint, ok := strings.CutSuffix(m, solanaToken2022Suffix); ok {
			mints = append(mints, solanaMint{mint: mint, program: indexer.SolanaTokenProgramIDs[1]})
		} else {
			mints = append(mints, solanaMint{mint: m, program: indexer.SolanaTokenProgramIDs[0]})
		}
	}
	return &SolanaAccountWorker{
		BaseWorker:        base,
		source:            source,
		loader:            loader,
		cursors:           cursors,
		mints:             mints,
		fullCheckInterval: interval,
		now:               time.Now,
		watched:           map[string]bool{},
		fingerprints:      map[string]string{},
		dirty:             map[string]bool{},
		cursorCache:       map[string]string{},
		recent:            newRecentSignatures(solanaRecentSignatures),
	}
}

func (w *SolanaAccountWorker) Start() {
	w.logger.Info("Starting Solana account watcher",
		"chain", w.chain.GetName(),
		"mints", len(w.mints),
		"full_check_interval", w.fullCheckInterval,
	)
	go w.run(w.poll)
}

// poll is one round: new addresses, balances, then the transactions of the accounts that changed.
func (w *SolanaAccountWorker) poll() error {
	ctx := w.ctx
	if err := w.ensureFloor(ctx); err != nil {
		return fmt.Errorf("floor slot: %w", err)
	}
	if err := w.loadNewOwners(ctx); err != nil {
		return fmt.Errorf("load addresses: %w", err)
	}

	if w.lastFull.IsZero() || w.now().Sub(w.lastFull) >= w.fullCheckInterval {
		w.lastFull = w.now()
		w.discoverTokenAccounts(ctx, w.owners)
		for _, acc := range w.accounts {
			w.dirty[acc] = true
		}
	}

	if len(w.accounts) == 0 {
		return nil
	}

	slot, fingerprints, err := w.source.AccountStates(ctx, w.accounts)
	if err != nil {
		return fmt.Errorf("read accounts: %w", err)
	}
	for i, acc := range w.accounts {
		if previous, seen := w.fingerprints[acc]; !seen || previous != fingerprints[i] {
			w.dirty[acc] = true
		}
		w.fingerprints[acc] = fingerprints[i]
	}

	var failed []string
	for _, acc := range w.accounts {
		if !w.dirty[acc] {
			continue
		}
		if err := w.syncAccount(ctx, acc); err != nil {
			// Stays dirty: retried next poll from its cursor.
			w.logger.Warn("Account sync failed; retrying next poll", "account", acc, "error", err)
			failed = append(failed, acc)
			continue
		}
		delete(w.dirty, acc)
	}

	indexed := slot
	if len(failed) > 0 {
		indexed = w.floor
		if latest, err := w.blockStore.GetLatestBlock(w.chain.GetNetworkInternalCode()); err == nil && latest > indexed {
			indexed = latest
		}
	} else {
		_ = w.blockStore.SaveLatestBlock(w.chain.GetNetworkInternalCode(), slot)
	}
	status.EnsureStatusRegistry(w.statusRegistry).UpdateHead(w.chain.GetName(), slot, indexed, w.now())
	if len(failed) > 0 {
		return fmt.Errorf("%d account(s) failed to sync, e.g. %s", len(failed), failed[0])
	}
	return nil
}

// syncAccount emits the monitored transfers of acc's transactions after its cursor, oldest first, moving the cursor
// after each one.
func (w *SolanaAccountWorker) syncAccount(ctx context.Context, acc string) error {
	cursor, err := w.cursor(acc)
	if err != nil {
		return err
	}
	sigs, err := w.source.NewSignatures(ctx, acc, cursor, w.floor)
	if err != nil {
		return err
	}
	for _, sig := range sigs {
		// A failed transaction moved nothing; one already processed through another account is skipped.
		if sig.Err == nil && !w.recent.has(sig.Signature) {
			block, err := w.source.TransactionBlock(ctx, sig.Signature)
			if err != nil {
				return fmt.Errorf("transaction %s: %w", sig.Signature, err)
			}
			if len(block.Transactions) > 0 {
				w.emitBlock(block)
			}
			w.recent.add(sig.Signature)
		}
		if err := w.setCursor(acc, sig.Signature); err != nil {
			return err
		}
	}
	return nil
}

// ensureFloor loads or sets the slot below which transactions are never taken.
func (w *SolanaAccountWorker) ensureFloor(ctx context.Context) error {
	if w.floorLoaded {
		return nil
	}
	key := w.key("floor")
	var floor uint64
	found, err := w.cursors.GetAny(key, &floor)
	if err != nil {
		return err
	}
	if !found {
		floor, err = w.blockStore.GetLatestBlock(w.chain.GetNetworkInternalCode())
		if err != nil {
			return err
		}
		if floor == 0 {
			if floor, err = w.chain.GetLatestBlockNumber(ctx); err != nil {
				return err
			}
		}
		if err := w.cursors.SetAny(key, floor); err != nil {
			return err
		}
		w.logger.Info("Solana account watcher starts after slot", "floor", floor)
	}
	w.floor = floor
	w.floorLoaded = true
	return nil
}

// loadNewOwners adds the addresses added to wallet_addresses since the last poll, with their token accounts.
func (w *SolanaAccountWorker) loadNewOwners(ctx context.Context) error {
	var added []string
	for {
		page, err := w.loader.LoadAddresses(ctx, AddressLoaderParams{
			NetworkType: enum.NetworkTypeSol,
			After:       w.loadedAfter,
			Limit:       solanaAddressPageSize,
		})
		if err != nil {
			return err
		}
		for _, a := range page {
			if a.CreatedAt.After(w.loadedAfter) {
				w.loadedAfter = a.CreatedAt
			}
			if w.watched[a.Address] {
				continue
			}
			w.owners = append(w.owners, a.Address)
			w.watch(a.Address)
			for _, m := range w.mints {
				ata, err := indexer.SolanaAssociatedTokenAddress(a.Address, m.mint, m.program)
				if err != nil {
					w.logger.Warn("Skipping token account of invalid address or mint", "address", a.Address, "mint", m.mint, "error", err)
					continue
				}
				w.watch(ata)
			}
			added = append(added, a.Address)
		}
		if len(page) < solanaAddressPageSize {
			break
		}
	}
	if len(added) > 0 && !w.lastFull.IsZero() {
		w.discoverTokenAccounts(ctx, added)
		w.logger.Info("Watching new addresses", "count", len(added), "accounts", len(w.accounts))
	}
	return nil
}

// discoverTokenAccounts watches the owners' existing token accounts that aren't associated accounts of token_mints.
func (w *SolanaAccountWorker) discoverTokenAccounts(ctx context.Context, owners []string) {
	for _, owner := range owners {
		accounts, err := w.source.TokenAccounts(ctx, owner)
		if err != nil {
			w.logger.Warn("Listing token accounts failed; retrying at the next full check", "owner", owner, "error", err)
			continue
		}
		for _, acc := range accounts {
			w.watch(acc)
		}
	}
}

func (w *SolanaAccountWorker) watch(acc string) {
	if w.watched[acc] {
		return
	}
	w.watched[acc] = true
	w.accounts = append(w.accounts, acc)
	w.dirty[acc] = true
}

func (w *SolanaAccountWorker) cursor(acc string) (string, error) {
	if c, ok := w.cursorCache[acc]; ok {
		return c, nil
	}
	var c string
	if _, err := w.cursors.GetAny(w.key("cursor/"+acc), &c); err != nil {
		return "", err
	}
	w.cursorCache[acc] = c
	return c, nil
}

func (w *SolanaAccountWorker) setCursor(acc, signature string) error {
	if err := w.cursors.SetAny(w.key("cursor/"+acc), signature); err != nil {
		return err
	}
	w.cursorCache[acc] = signature
	return nil
}

func (w *SolanaAccountWorker) key(suffix string) string {
	return "solana_accounts/" + w.chain.GetNetworkInternalCode() + "/" + suffix
}

// recentSignatures remembers the last n processed signatures, so a transaction touching several watched accounts
// (an owner and its token account) is emitted once.
type recentSignatures struct {
	max   int
	order []string
	set   map[string]bool
}

func newRecentSignatures(max int) *recentSignatures {
	return &recentSignatures{max: max, set: map[string]bool{}}
}

func (r *recentSignatures) has(sig string) bool { return r.set[sig] }

func (r *recentSignatures) add(sig string) {
	if r.set[sig] {
		return
	}
	r.set[sig] = true
	r.order = append(r.order, sig)
	if len(r.order) > r.max {
		delete(r.set, r.order[0])
		r.order = slices.Delete(r.order, 0, 1)
	}
}
