package indexer

import (
	"fmt"
	"log/slog"

	"github.com/fystack/indexer/internal/chains"
	"github.com/fystack/indexer/internal/chains/tron"
	"github.com/fystack/indexer/internal/config"
	"github.com/fystack/indexer/internal/events"
)

type Manager struct {
	config  *config.Config
	emitter *events.Emitter
	workers []*Worker
}

func NewManager(cfg *config.Config) (*Manager, error) {
	emitter, err := events.NewEmitter(cfg.Indexer.NATS.URL, cfg.Indexer.NATS.SubjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to create event emitter: %w", err)
	}

	return &Manager{
		config:  cfg,
		emitter: emitter,
	}, nil
}

func (m *Manager) Start() error {
	for chainName, chainConfig := range m.config.Indexer.Chains {
		var chainIndexer chains.ChainIndexer

		switch chainName {
		case "tron":
			chainIndexer = tron.NewIndexer(chainConfig.Nodes)
		default:
			return fmt.Errorf("unsupported chain: %s", chainName)
		}

		worker := NewWorker(chainIndexer, chainConfig, m.emitter)
		worker.Start()
		m.workers = append(m.workers, worker)
	}

	slog.Info("Started indexer", "chains", len(m.workers))
	return nil
}

func (m *Manager) Stop() {
	for _, worker := range m.workers {
		worker.Stop()
	}
	m.emitter.Close()
}
