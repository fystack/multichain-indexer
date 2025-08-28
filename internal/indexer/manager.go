package indexer

import (
	"context"
	"fmt"

	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
	"github.com/fystack/transaction-indexer/pkg/ratelimiter"
)

type Manager struct {
	ctx        context.Context
	cfg        *config.Config
	store      infra.KVStore
	blockStore *BlockStore
	emitter    *events.Emitter
	workers    []Worker
	addressBF  addressbloomfilter.WalletAddressBloomFilter
}

func NewManager(ctx context.Context, cfg *config.Config) (*Manager, error) {
	store, err := kvstore.NewFromConfig(cfg.KVStore)
	if err != nil {
		return nil, err
	}
	logger.Info("KVStore initialized", "store", store.GetName())
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
func (m *Manager) Start(chainNames []string, mode WorkerMode) error {
	for _, chainName := range chainNames {
		chainCfg, err := m.cfg.GetChain(chainName)
		if err != nil {
			return fmt.Errorf("get chain %s: %w", chainName, err)
		}
		idxr, err := m.createIndexer(chainCfg.Type, chainCfg)
		if err != nil {
			return fmt.Errorf("create indexer for %s: %w", chainCfg.Name, err)
		}
		w := createWorkerByMode(m.ctx, idxr, chainCfg, m.store, m.blockStore, m.emitter, m.addressBF, mode)
		if w == nil {
			return fmt.Errorf("unsupported or uninitialized worker mode: %s", mode)
		}
		w.Start()
		m.workers = append(m.workers, w)
		logger.Info("Started worker", "chain", chainCfg.Name, "type", chainCfg.Type, "mode", mode)
	}
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
	// Clean up global rate limiters
	ratelimiter.CloseAllRateLimiters()
	logger.Info("Manager stopped")
}

func (m *Manager) createIndexer(chainType enum.ChainType, cfg config.ChainConfig) (Indexer, error) {
	switch chainType {
	case enum.ChainTypeEVM:
		return NewEVMIndexer(cfg)
	case enum.ChainTypeTron:
		return NewTronIndexer(cfg)
	default:
		return nil, fmt.Errorf("unsupported chain: %s", chainType)
	}
}

func createWorkerByMode(ctx context.Context, chain Indexer, config config.ChainConfig, kv infra.KVStore, blockStore *BlockStore, emitter *events.Emitter, addressBF addressbloomfilter.WalletAddressBloomFilter, mode WorkerMode) Worker {
	switch mode {
	case ModeRegular:
		return NewRegularWorker(ctx, chain, config, kv, blockStore, emitter, addressBF)
	case ModeCatchup:
		return NewCatchupWorker(ctx, chain, config, kv, blockStore, emitter, addressBF)
	case ModeRescanner:
		// Build a base worker for rescanner and wrap with a window (e.g., 64 blocks)
		bw := newWorkerWithMode(ctx, chain, config, kv, blockStore, emitter, addressBF, ModeRescanner)
		return NewRescannerWorker(bw, 64)
	default:
		return nil
	}
}
