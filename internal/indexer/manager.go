package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
)

type Manager struct {
	ctx        context.Context
	cfg        *config.Config
	store      kvstore.KVStore
	blockStore *BlockStore
	emitter    *events.Emitter
	workers    []*Worker
	addressBF  addressbloomfilter.WalletAddressBloomFilter
}

func NewManager(ctx context.Context, cfg *config.Config) (*Manager, error) {
	store, err := kvstore.NewBadgerStore(cfg.Storage.Directory)
	if err != nil {
		return nil, fmt.Errorf("badger init: %w", err)
	}
	emitter, err := events.NewEmitter(cfg.NATS.URL, cfg.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("emitter init: %w", err)
	}

	addressBF := addressbloomfilter.NewBloomFilter(cfg.BloomFilter)
	addressBF.Initialize(ctx)

	return &Manager{
		ctx:        ctx,
		cfg:        cfg,
		store:      store,
		blockStore: NewBlockStore(store),
		emitter:    emitter,
		addressBF:  addressBF,
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
		if len(targetChains) > 0 && !slices.Contains(targetChains, name) {
			continue
		}

		idxr, err := m.createIndexer(enum.ChainType(name), chainCfg)
		if err != nil {
			return fmt.Errorf("create indexer for %s: %w", name, err)
		}
		w := NewWorker(m.ctx, idxr, chainCfg, m.store, m.blockStore, m.emitter, m.addressBF)
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

	idxr, err := m.createIndexer(enum.ChainType(chainName), cfg)
	if err != nil {
		return err
	}

	// load start block from KV (default to 1 if not found)
	var startBlockNum uint64 = 1
	if startBlock, err := m.blockStore.GetLatestBlock(cfg.Name.String()); err == nil {
		startBlockNum = startBlock + 1 // Start from next block after last processed
	}

	// fetch end block from RPC
	endBlock, err := idxr.GetLatestBlockNumber(m.ctx)
	if err != nil {
		return fmt.Errorf("rpc latest block: %w", err)
	}

	if startBlockNum == 1 && endBlock > constant.MaxCatchupBlocks {
		startBlockNum = endBlock - constant.MaxCatchupBlocks
		slog.Info("Limiting catchup range for fresh start", "chain", chainName,
			"original_start", 1, "adjusted_start", startBlockNum)
	}

	if startBlockNum >= endBlock {
		slog.Info("No catchup needed", "chain", chainName, "current", startBlockNum, "latest", endBlock)
		return nil
	}

	// start catchup worker
	w := NewCatchupWorker(m.ctx, idxr, cfg, m.store, m.blockStore, m.emitter, m.addressBF, startBlockNum, endBlock)
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
	if m.blockStore != nil {
		_ = m.blockStore.Close()
		m.blockStore = nil
	}
	slog.Info("Manager stopped")
}

func (m *Manager) createIndexer(name enum.ChainType, cfg config.ChainConfig) (Indexer, error) {
	switch name {
	case enum.ChainTypeEVM:
		return NewEVMIndexer(cfg)
	case enum.ChainTypeTron:
		return NewTronIndexer(cfg)
	default:
		return nil, fmt.Errorf("unsupported chain: %s", name)
	}
}

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
