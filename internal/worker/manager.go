package worker

import (
	"context"
	"fmt"
	"sort"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

type Manager struct {
	workers     []Worker
	kvstore     infra.KVStore
	blockStore  blockstore.Store
	emitter     events.Emitter
	pubkeyStore pubkeystore.Store
}

func NewManager(
	kvstore infra.KVStore,
	blockStore blockstore.Store,
	emitter events.Emitter,
	pubkeyStore pubkeystore.Store,
) *Manager {
	return &Manager{
		kvstore:     kvstore,
		blockStore:  blockStore,
		emitter:     emitter,
		pubkeyStore: pubkeyStore,
	}
}

// Start launches all injected workers.
func (m *Manager) Start() {
	for _, w := range m.workers {
		w.Start()
	}
}

// Stop shuts down all workers + resources.
func (m *Manager) Stop() {
	for _, w := range m.workers {
		if w != nil {
			w.Stop()
		}
	}

	if m.emitter != nil {
		m.emitter.Close()
	}
	if m.blockStore != nil {
		if err := m.blockStore.Close(); err != nil {
			logger.Error("Failed to close block store", "err", err)
		}
	}
	if m.pubkeyStore != nil {
		if err := m.pubkeyStore.Close(); err != nil {
			logger.Error("Failed to close pubkey store", "err", err)
		}
	}
	if m.kvstore != nil {
		if err := m.kvstore.Close(); err != nil {
			logger.Error("Failed to close KV store", "err", err)
		}
	}

	ratelimiter.CloseAllRateLimiters()
	logger.Info("Manager stopped")
}

// AddWorkers injects workers into manager.
func (m *Manager) AddWorkers(workers ...Worker) {
	m.workers = append(m.workers, workers...)
}

// Workers returns a copy of managed workers.
func (m *Manager) Workers() []Worker {
	out := make([]Worker, 0, len(m.workers))
	out = append(out, m.workers...)
	return out
}

// TonWalletReloadService handles runtime wallet cache reload for TON workers.
type TonWalletReloadService struct {
	reloaders []TonWalletReloader
}

// TonJettonReloadService handles runtime jetton registry reload for TON workers.
type TonJettonReloadService struct {
	reloaders []TonJettonReloader
}

func NewTonWalletReloadService(workers []Worker) *TonWalletReloadService {
	reloaders := make([]TonWalletReloader, 0)
	for _, w := range workers {
		reloader, ok := w.(TonWalletReloader)
		if !ok || reloader.GetNetworkType() != enum.NetworkTypeTon {
			continue
		}
		reloaders = append(reloaders, reloader)
	}

	return &TonWalletReloadService{reloaders: reloaders}
}

func NewTonWalletReloadServiceFromManager(m *Manager) *TonWalletReloadService {
	if m == nil {
		return &TonWalletReloadService{}
	}
	return NewTonWalletReloadService(m.Workers())
}

func NewTonJettonReloadService(workers []Worker) *TonJettonReloadService {
	reloaders := make([]TonJettonReloader, 0)
	for _, w := range workers {
		reloader, ok := w.(TonJettonReloader)
		if !ok || reloader.GetNetworkType() != enum.NetworkTypeTon {
			continue
		}
		reloaders = append(reloaders, reloader)
	}
	return &TonJettonReloadService{reloaders: reloaders}
}

func NewTonJettonReloadServiceFromManager(m *Manager) *TonJettonReloadService {
	if m == nil {
		return &TonJettonReloadService{}
	}
	return NewTonJettonReloadService(m.Workers())
}

func (s *TonWalletReloadService) ReloadTonWallets(
	ctx context.Context,
	req TonWalletReloadRequest,
) ([]TonWalletReloadResult, error) {
	source := req.Source.Normalize()
	results := make([]TonWalletReloadResult, 0)

	for _, reloader := range s.reloaders {
		chainName := reloader.GetName()
		if req.ChainFilter != "" && req.ChainFilter != chainName {
			continue
		}

		item := TonWalletReloadResult{Chain: chainName}
		var (
			count int
			err   error
		)
		switch source {
		case WalletReloadSourceDB:
			count, err = reloader.ReloadWalletsFromDB(ctx)
		default:
			count, err = reloader.ReloadWalletsFromKV(ctx)
		}

		if err != nil {
			item.Error = err.Error()
		} else {
			item.ReloadedWallets = count
		}
		results = append(results, item)
	}

	if len(results) == 0 {
		if req.ChainFilter != "" {
			return nil, fmt.Errorf("%w: %s", ErrTonWorkerNotFound, req.ChainFilter)
		}
		return nil, ErrNoTonWorkerConfigured
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Chain < results[j].Chain })
	return results, nil
}

func (s *TonJettonReloadService) ReloadTonJettons(
	ctx context.Context,
	req TonJettonReloadRequest,
) ([]TonJettonReloadResult, error) {
	results := make([]TonJettonReloadResult, 0)

	for _, reloader := range s.reloaders {
		chainName := reloader.GetName()
		if req.ChainFilter != "" && req.ChainFilter != chainName {
			continue
		}

		item := TonJettonReloadResult{Chain: chainName}
		count, err := reloader.ReloadJettons(ctx)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.ReloadedJettons = count
		}
		results = append(results, item)
	}

	if len(results) == 0 {
		if req.ChainFilter != "" {
			return nil, fmt.Errorf("%w: %s", ErrTonWorkerNotFound, req.ChainFilter)
		}
		return nil, ErrNoTonWorkerConfigured
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Chain < results[j].Chain })
	return results, nil
}
