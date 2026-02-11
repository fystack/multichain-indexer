package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

var (
	ErrNoTonWorkerConfigured = errors.New("no ton worker configured")
	ErrTonWorkerNotFound     = errors.New("ton worker not found")
)

type TonJettonReloadRequest struct {
	ChainFilter string
}

type TonJettonReloadResult struct {
	Chain           string `json:"chain"`
	ReloadedJettons int    `json:"reloaded_jettons"`
	Error           string `json:"error,omitempty"`
}

// TonJettonReloader is implemented by TON workers that support runtime jetton reload.
type TonJettonReloader interface {
	Worker
	GetName() string
	GetNetworkType() enum.NetworkType
	ReloadJettons(ctx context.Context) (int, error)
}

// TonJettonReloadService handles runtime jetton registry reload for TON workers.
type TonJettonReloadService struct {
	reloaders []TonJettonReloader
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
