package indexer

import (
	"fmt"
	"log/slog"

	"idx/internal/core"
	"idx/internal/events"
	"idx/internal/kvstore"
	"idx/internal/rpc"
)

type Manager struct {
	config  *core.Config
	kvstore kvstore.KVStore
	emitter *events.Emitter
	workers []*Worker
}

func NewManager(cfg *core.Config) (*Manager, error) {
	emitter, err := events.NewEmitter(cfg.Indexer.NATS.URL, cfg.Indexer.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to create event emitter: %w", err)
	}
	slog.Info("Event emitter created successfully")
	// Initialize BadgerStore (load from config)
	store, err := kvstore.NewBadgerStore(cfg.Indexer.Storage.Directory)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger store: %w", err)
	}
	return &Manager{
		config:  cfg,
		kvstore: store,
		emitter: emitter,
	}, nil
}

// Start starts all networks if chainName is empty, or a specific network if chainName is provided.
func (m *Manager) Start(chainNameOpt ...string) error {
	var chainsToStart map[string]core.ChainConfig
	if len(chainNameOpt) > 0 && chainNameOpt[0] != "" {
		name := chainNameOpt[0]
		cfg, ok := m.config.Indexer.Chains[name]
		if !ok {
			return fmt.Errorf("chain not found in config: %s", name)
		}
		chainsToStart = map[string]core.ChainConfig{name: cfg}
	} else {
		chainsToStart = m.config.Indexer.Chains
	}

	for chainName, chainConfig := range chainsToStart {
		var chainIndexer Indexer
		switch chainName {
		case rpc.NetworkEVM:
			evmIdx, err := NewEVMIndexer(chainConfig)
			if err != nil {
				return fmt.Errorf("create EVM indexer: %w", err)
			}
			chainIndexer = evmIdx
		case rpc.NetworkTron, rpc.NetworkSolana, rpc.NetworkBitcoin, rpc.NetworkGeneric:
			return fmt.Errorf("indexer not implemented for chain: %s", chainName)
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
