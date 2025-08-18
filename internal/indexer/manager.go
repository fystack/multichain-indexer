package indexer

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/fystack/transaction-indexer/internal/core"
	"github.com/fystack/transaction-indexer/internal/events"
	"github.com/fystack/transaction-indexer/internal/kvstore"
	"github.com/fystack/transaction-indexer/internal/rpc"
)

type Manager struct {
	config             *core.Config
	kvstore            kvstore.KVStore
	emitter            *events.Emitter
	workers            []*Worker
	failedBlockWorkers []*Worker
}

func NewManager(cfg *core.Config) (*Manager, error) {
	emitter, err := events.NewEmitter(cfg.NATS.URL, cfg.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to create event emitter: %w", err)
	}
	slog.Info("Event emitter created successfully")
	// Initialize BadgerStore (load from config)
	store, err := kvstore.NewBadgerStore(cfg.Storage.Directory)
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
func (m *Manager) Start(retryFailed bool, chainNameOpt ...string) error {
	chainsToStart, err := m.getChainsToStart(chainNameOpt...)
	if err != nil {
		return err
	}

	for chainName, chainConfig := range chainsToStart {
		chainIndexer, err := m.createIndexer(chainName, chainConfig)
		if err != nil {
			return err
		}

		worker := NewWorker(chainIndexer, chainConfig, m.kvstore, m.emitter)
		worker.Start(retryFailed)
		m.workers = append(m.workers, worker)
		slog.Info("Started indexer", "chain", chainName)
	}

	return nil
}

// StartFailedBlocks starts failed block processing for all networks if chainName is empty, or a specific network if chainName is provided.
func (m *Manager) StartFailedBlocks(chainNameOpt ...string) error {
	chainsToStart, err := m.getChainsToStart(chainNameOpt...)
	if err != nil {
		return err
	}

	for chainName, chainConfig := range chainsToStart {
		chainIndexer, err := m.createIndexer(chainName, chainConfig)
		if err != nil {
			return err
		}

		worker := NewFailedBlockWorker(chainIndexer, chainConfig, m.kvstore, m.emitter)
		worker.StartFailedBlocksOneShot()
		m.failedBlockWorkers = append(m.failedBlockWorkers, worker)
		slog.Info("Started failed block processing (one-shot)", "chain", chainName)
	}

	return nil
}

// StartFailedBlocksContinuous starts continuous failed block processing (keeps running)
func (m *Manager) StartFailedBlocksContinuous(chainNameOpt ...string) error {
	chainsToStart, err := m.getChainsToStart(chainNameOpt...)
	if err != nil {
		return err
	}

	for chainName, chainConfig := range chainsToStart {
		chainIndexer, err := m.createIndexer(chainName, chainConfig)
		if err != nil {
			return err
		}

		worker := NewFailedBlockWorker(chainIndexer, chainConfig, m.kvstore, m.emitter)
		worker.StartFailedBlocks() // Use continuous mode
		m.failedBlockWorkers = append(m.failedBlockWorkers, worker)
		slog.Info("Started failed block processing (continuous)", "chain", chainName)
	}

	return nil
}

// GetFailedBlocksStatus returns the status of failed blocks for all chains
func (m *Manager) GetFailedBlocksStatus() (map[string]int, error) {
	status := make(map[string]int)
	allWorkers := append(m.workers, m.failedBlockWorkers...)

	for _, worker := range allWorkers {
		count, err := worker.GetFailedBlocksCount()
		if err != nil {
			slog.Error("Failed to get failed blocks count", "chain", worker.chain.GetName(), "error", err)
			continue
		}
		status[worker.chain.GetName()] = count
	}

	return status, nil
}

// CleanupResolvedBlocks cleans up old resolved blocks for all chains
func (m *Manager) CleanupResolvedBlocks(olderThan time.Duration) error {
	allWorkers := append(m.workers, m.failedBlockWorkers...)
	var errors []error

	for _, worker := range allWorkers {
		if err := worker.CleanupOldResolvedBlocks(olderThan); err != nil {
			slog.Error("Failed to cleanup resolved blocks", "chain", worker.chain.GetName(), "error", err)
			errors = append(errors, fmt.Errorf("chain %s: %w", worker.chain.GetName(), err))
		} else {
			slog.Info("Cleaned up resolved blocks", "chain", worker.chain.GetName())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup failed for %d chains: %v", len(errors), errors)
	}
	return nil
}

// StopFailedBlocks stops all failed block processing workers
func (m *Manager) StopFailedBlocks() {
	for _, worker := range m.failedBlockWorkers {
		worker.Stop()
		slog.Info("Stopped failed block processing", "chain", worker.chain.GetName())
	}
	m.failedBlockWorkers = nil
}

func (m *Manager) Stop() {
	// Stop regular workers
	for _, worker := range m.workers {
		worker.Stop()
	}

	// Stop failed block workers
	m.StopFailedBlocks()

	m.emitter.Close()
}

// getChainsToStart returns the chains to start based on the provided chain name options
func (m *Manager) getChainsToStart(chainNameOpt ...string) (map[string]core.ChainConfig, error) {
	if len(chainNameOpt) > 0 && chainNameOpt[0] != "" {
		name := chainNameOpt[0]
		cfg, ok := m.config.Chains.Items[name]
		if !ok {
			return nil, fmt.Errorf("chain not found in config: %s", name)
		}
		return map[string]core.ChainConfig{name: cfg}, nil
	}
	return m.config.Chains.Items, nil
}

// createIndexer creates an indexer for the specified chain
func (m *Manager) createIndexer(chainName string, chainConfig core.ChainConfig) (Indexer, error) {
	switch chainName {
	case rpc.NetworkEVM:
		evmIdx, err := NewEVMIndexer(chainConfig)
		if err != nil {
			return nil, fmt.Errorf("create EVM indexer: %w", err)
		}
		return evmIdx, nil
	case rpc.NetworkTron:
		tronIdx, err := NewTronIndexer(chainConfig)
		if err != nil {
			return nil, fmt.Errorf("create Tron indexer: %w", err)
		}
		return tronIdx, nil
	case rpc.NetworkSolana, rpc.NetworkBitcoin, rpc.NetworkGeneric:
		return nil, fmt.Errorf("indexer not implemented for chain: %s", chainName)
	default:
		return nil, fmt.Errorf("unsupported chain: %s", chainName)
	}
}
