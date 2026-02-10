package worker

import (
	"context"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

const defaultShutdownTimeout = 30 * time.Second

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

// Stop shuts down all workers concurrently with a timeout, then closes resources.
func (m *Manager) Stop() {
	// Stop all workers concurrently with timeout
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, w := range m.workers {
			if w != nil {
				wg.Add(1)
				go func(w Worker) {
					defer wg.Done()
					w.Stop()
				}(w)
			}
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("All workers stopped")
	case <-time.After(defaultShutdownTimeout):
		logger.Warn("Worker shutdown timed out, proceeding with resource cleanup",
			"timeout", defaultShutdownTimeout)
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
