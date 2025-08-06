package indexer

import (
	"fmt"
	"log/slog"

	"github.com/fystack/transaction-indexer/internal/chains"
	"github.com/fystack/transaction-indexer/internal/chains/evm"
	"github.com/fystack/transaction-indexer/internal/chains/tron"
	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/internal/kvstore"
)

type Manager struct {
	config  *config.Config
	kvstore *kvstore.BadgerStore
	emitter *events.Emitter
	workers []*Worker
}

func NewManager(cfg *config.Config) (*Manager, error) {
	emitter, err := events.NewEmitter(cfg.Indexer.NATS.URL, cfg.Indexer.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to create event emitter: %w", err)
	}
	slog.Info("Event emitter created successfully")
	// Initialize BadgerStore (use a path like ./badger_data)
	store, err := kvstore.NewBadgerStore("badger_data")
	if err != nil {
		return nil, fmt.Errorf("failed to open badger store: %w", err)
	}
	return &Manager{
		config:  cfg,
		kvstore: store,
		emitter: emitter,
	}, nil
}

// Start starts all chains if chainName is empty, or a specific chain if chainName is provided.
func (m *Manager) Start(chainNameOpt ...string) error {
	var chainsToStart map[string]config.ChainConfig
	if len(chainNameOpt) > 0 && chainNameOpt[0] != "" {
		name := chainNameOpt[0]
		cfg, ok := m.config.Indexer.Chains[name]
		if !ok {
			return fmt.Errorf("chain not found in config: %s", name)
		}
		chainsToStart = map[string]config.ChainConfig{name: cfg}
	} else {
		chainsToStart = m.config.Indexer.Chains
	}

	for chainName, chainConfig := range chainsToStart {
		var chainIndexer chains.ChainIndexer
		switch chainName {
		case chains.ChainTron:
			chainIndexer = tron.NewIndexerWithConfig(chainConfig.Nodes, chainConfig)
		case chains.ChainEVM:
			chainIndexer = evm.NewIndexerWithConfig(chainConfig.Nodes, chainConfig)
		default:
			return fmt.Errorf("unsupported chain: %s", chainName)
		}

		worker := NewWorker(chainIndexer, chainConfig, m.kvstore, m.emitter)
		worker.Start()
		m.workers = append(m.workers, worker)
	}

	return nil
}

func (m *Manager) Stop() {
	for _, worker := range m.workers {
		worker.Stop()
	}
	m.emitter.Close()
}
