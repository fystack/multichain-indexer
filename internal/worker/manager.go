package worker

import (
	"context"
	"fmt"

	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/blockstore"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
	"github.com/fystack/transaction-indexer/pkg/pubkeystore"
	"github.com/fystack/transaction-indexer/pkg/ratelimiter"
	"gorm.io/gorm"
)

type FailedBlockEvent struct {
	Chain   string
	Block   uint64
	Attempt int
}

type Manager struct {
	ctx         context.Context
	cfg         *config.Config
	store       infra.KVStore
	blockStore  *blockstore.Store
	emitter     *events.Emitter
	workers     []Worker
	pubkeyStore pubkeystore.Store
	failedChan  chan FailedBlockEvent
}

func NewManager(ctx context.Context, cfg *config.Config, db *gorm.DB, redisClient infra.RedisClient) (*Manager, error) {
	store, err := kvstore.NewFromConfig(cfg.KVStore)
	if err != nil {
		return nil, err
	}
	logger.Info("KVStore initialized", "store", store.GetName())
	emitter, err := events.NewEmitter(cfg.NATS.URL, cfg.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("emitter init: %w", err)
	}

	addressBF := addressbloomfilter.NewBloomFilter(cfg.BloomFilter, db, redisClient)
	if err := addressBF.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("address bloom filter init: %w", err)
	}
	pubkeyStore := pubkeystore.NewPublicKeyStore(store, addressBF)

	return &Manager{
		ctx:         ctx,
		cfg:         cfg,
		store:       store,
		blockStore:  blockstore.NewBlockStore(store),
		emitter:     emitter,
		pubkeyStore: pubkeyStore,
		failedChan:  make(chan FailedBlockEvent, 1000),
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
		w := m.createWorkerByMode(idxr, chainCfg, mode)
		if w == nil {
			return fmt.Errorf("unsupported or uninitialized worker mode: %s", mode)
		}
		w.Start()
		m.workers = append(m.workers, w)
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

func (m *Manager) createIndexer(chainType enum.ChainType, cfg config.ChainConfig) (indexer.Indexer, error) {
	switch chainType {
	case enum.ChainTypeEVM:
		return indexer.NewEVMIndexer(cfg)
	case enum.ChainTypeTron:
		return indexer.NewTronIndexer(cfg)
	default:
		return nil, fmt.Errorf("unsupported chain: %s", chainType)
	}
}

func (m *Manager) createWorkerByMode(chain indexer.Indexer, config config.ChainConfig, mode WorkerMode) Worker {
	switch mode {
	case ModeRegular:
		return NewRegularWorker(m.ctx, chain, config, m.store, m.blockStore, m.emitter, m.pubkeyStore, m.failedChan)
	case ModeCatchup:
		return NewCatchupWorker(m.ctx, chain, config, m.store, m.blockStore, m.emitter, m.pubkeyStore, m.failedChan)
	case ModeRescanner:
		return NewRescannerWorker(m.ctx, chain, config, m.store, m.blockStore, m.emitter, m.pubkeyStore, m.failedChan)
	default:
		return nil
	}
}
