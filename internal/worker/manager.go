package worker

import (
	"context"

	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/ratelimiter"
	"github.com/fystack/transaction-indexer/pkg/store/blockstore"
	"github.com/fystack/transaction-indexer/pkg/store/pubkeystore"
)

type Manager struct {
	ctx         context.Context
	workers     []Worker
	kvstore     infra.KVStore
	blockStore  blockstore.Store
	emitter     events.Emitter
	pubkeyStore pubkeystore.Store
	failedChan  chan FailedBlockEvent
}

func NewManager(
	ctx context.Context,
	kvstore infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
	failedChan chan FailedBlockEvent,
) *Manager {
	return &Manager{
		ctx:         ctx,
		kvstore:     kvstore,
		blockStore:  blockStore,
		emitter:     emitter,
		pubkeyStore: pubkeyStore,
		failedChan:  failedChan,
	}
}

// Start launches all injected workers
func (m *Manager) Start() {
	for _, w := range m.workers {
		w.Start()
	}
}

// Stop shuts down all workers + resources
func (m *Manager) Stop() {
	// Stop all workers
	for _, w := range m.workers {
		if w != nil {
			w.Stop()
		}
	}

	// Close resources
	m.closeResource("emitter", m.emitter, func() error { m.emitter.Close(); return nil })
	m.closeResource("block store", m.blockStore, m.blockStore.Close)
	m.closeResource("pubkey store", m.pubkeyStore, m.pubkeyStore.Close)
	m.closeResource("KV store", m.kvstore, m.kvstore.Close)

	// Close all rate limiters
	ratelimiter.CloseAllRateLimiters()

	logger.Info("Manager stopped")
}

// closeResource is a helper to close resources with consistent error handling
func (m *Manager) closeResource(name string, resource interface{}, closer func() error) {
	if resource != nil {
		if err := closer(); err != nil {
			logger.Error("Failed to close "+name, "err", err)
		}
	}
}

// Inject workers into manager
func (m *Manager) AddWorkers(workers ...Worker) {
	m.workers = append(m.workers, workers...)
}
