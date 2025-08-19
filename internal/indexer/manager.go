package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

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
func (m *Manager) Start() error {
	for name, chainCfg := range m.cfg.Chains.Items {
		idxr, err := m.createIndexer(name, chainCfg)
		if err != nil {
			return err
		}
		w := NewWorker(idxr, chainCfg, m.store, m.emitter)
		w.Start()
		m.workers = append(m.workers, w)
		slog.Info("Started regular worker", "chain", name)
	}
	return nil
}

// StartCatchupAuto runs a catchup worker for a chain (auto range from KV -> RPC head)
func (m *Manager) StartCatchupAuto(chainName string) error {
	cfg, ok := m.cfg.Chains.Items[chainName]
	if !ok {
		return fmt.Errorf("chain not in config: %s", chainName)
	}

	idxr, err := m.createIndexer(chainName, cfg)
	if err != nil {
		return err
	}

	// load start block from KV
	startBlock, err := m.store.Get(fmt.Sprintf("latest_block_%s", cfg.Name))
	if err != nil {
		return fmt.Errorf("get last block from kv: %w", err)
	}
	startBlockNum, err := strconv.ParseUint(string(startBlock), 10, 64)
	if err != nil {
		return fmt.Errorf("parse start block: %w", err)
	}

	// fetch end block from RPC
	ctx := context.Background()
	endBlock, err := idxr.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("rpc latest block: %w", err)
	}

	if startBlockNum >= endBlock {
		slog.Info("No catchup needed", "chain", chainName, "block", startBlockNum)
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
