package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/fystack/transaction-indexer/internal/core"
	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/internal/kvstore"
)

type Manager struct {
	cfg     *core.Config
	store   kvstore.KVStore
	emitter *events.Emitter
	workers []*Worker
}

func NewManager(cfg *core.Config) (*Manager, error) {
	store, err := kvstore.NewBadgerStore(cfg.Storage.Directory)
	if err != nil {
		return nil, fmt.Errorf("badger init: %w", err)
	}
	emitter, err := events.NewEmitter(cfg.NATS.URL, cfg.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("emitter init: %w", err)
	}
	return &Manager{
		cfg:     cfg,
		store:   store,
		emitter: emitter,
	}, nil
}

// Start kicks off all regular workers (one per chain)
func (m *Manager) Start(chainName ...string) error {
	// Parse comma-separated chain names if provided
	var targetChains []string
	if len(chainName) > 0 && chainName[0] != "" {
		targetChains = parseChainNames(chainName[0])
	}

	for name, chainCfg := range m.cfg.Chains.Items {
		// If specific chains requested, only start those
		if len(targetChains) > 0 && !contains(targetChains, name) {
			continue
		}

		idxr, err := m.createIndexer(name, chainCfg)
		if err != nil {
			return fmt.Errorf("create indexer for %s: %w", name, err)
		}
		w := NewWorker(idxr, chainCfg, m.store, m.emitter)
		w.Start()
		m.workers = append(m.workers, w)
		slog.Info("Started regular worker", "chain", name)
	}
	return nil
}

// StartCatchupAuto runs catchup workers for chains (auto range from KV -> RPC head)
func (m *Manager) StartCatchupAuto(chainNames string) error {
	// Parse comma-separated chain names
	targetChains := parseChainNames(chainNames)

	var errors []error
	for _, chainName := range targetChains {
		if err := m.startCatchupForChain(chainName); err != nil {
			slog.Error("Failed to start catchup", "chain", chainName, "error", err)
			errors = append(errors, fmt.Errorf("chain %s: %w", chainName, err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to start catchup for some chains: %v", errors)
	}
	return nil
}

// startCatchupForChain starts catchup for a single chain
func (m *Manager) startCatchupForChain(chainName string) error {
	cfg, ok := m.cfg.Chains.Items[chainName]
	if !ok {
		return fmt.Errorf("chain not in config: %s", chainName)
	}

	idxr, err := m.createIndexer(chainName, cfg)
	if err != nil {
		return err
	}

	// load start block from KV (default to 1 if not found)
	var startBlockNum uint64 = 1
	if startBlock, err := m.store.Get(fmt.Sprintf("latest_block_%s", cfg.Name)); err == nil {
		if parsed, err := strconv.ParseUint(string(startBlock), 10, 64); err == nil {
			startBlockNum = parsed + 1 // Start from next block after last processed
		}
	}

	// fetch end block from RPC
	ctx := context.Background()
	endBlock, err := idxr.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("rpc latest block: %w", err)
	}

	// For fresh starts with large block numbers, limit catchup range
	const maxCatchupBlocks = 100000
	if startBlockNum == 1 && endBlock > maxCatchupBlocks {
		startBlockNum = endBlock - maxCatchupBlocks
		slog.Info("Limiting catchup range for fresh start", "chain", chainName,
			"original_start", 1, "adjusted_start", startBlockNum)
	}

	if startBlockNum >= endBlock {
		slog.Info("No catchup needed", "chain", chainName, "current", startBlockNum, "latest", endBlock)
		return nil
	}

	// start catchup worker
	w := NewCatchupWorker(idxr, cfg, m.store, m.emitter, startBlockNum, endBlock)
	w.Start()
	m.workers = append(m.workers, w)

	slog.Info("Started catchup worker",
		"chain", chainName,
		"start", startBlockNum,
		"end", endBlock,
	)
	return nil
}

// Stop shuts down all workers + resources
func (m *Manager) Stop() {
	for _, w := range m.workers {
		w.Stop()
	}
	if m.emitter != nil {
		m.emitter.Close()
	}
	slog.Info("Manager stopped")
}

// --- helper for chain-specific indexers
func (m *Manager) createIndexer(name string, cfg core.ChainConfig) (Indexer, error) {
	switch name {
	case "evm":
		return NewEVMIndexer(cfg)
	case "tron":
		return NewTronIndexer(cfg)
	default:
		return nil, fmt.Errorf("unsupported chain: %s", name)
	}
}

// parseChainNames parses comma-separated chain names and returns a slice
func parseChainNames(chainNames string) []string {
	if chainNames == "" {
		return nil
	}

	chains := strings.Split(chainNames, ",")
	var result []string
	for _, chain := range chains {
		chain = strings.TrimSpace(chain)
		if chain != "" {
			result = append(result, chain)
		}
	}
	return result
}

// contains checks if a string slice contains a specific string
func contains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}
