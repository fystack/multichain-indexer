package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/enum"
)

var (
	ErrNoTonWorkerConfigured = errors.New("no ton worker configured")
	ErrTonWorkerNotFound     = errors.New("ton worker not found")
)

type TonJettonReloadRequest struct {
	ChainFilter string
}

type WalletReloadSource string

const (
	WalletReloadSourceKV WalletReloadSource = "kv"
	WalletReloadSourceDB WalletReloadSource = "db"
)

func (s WalletReloadSource) Normalize() WalletReloadSource {
	switch strings.ToLower(strings.TrimSpace(string(s))) {
	case string(WalletReloadSourceDB):
		return WalletReloadSourceDB
	case string(WalletReloadSourceKV), "":
		return WalletReloadSourceKV
	default:
		return WalletReloadSource(strings.ToLower(strings.TrimSpace(string(s))))
	}
}

func (s WalletReloadSource) IsValid() bool {
	normalized := s.Normalize()
	return normalized == WalletReloadSourceKV || normalized == WalletReloadSourceDB
}

type TonWalletReloadRequest struct {
	Source      WalletReloadSource
	ChainFilter string
}

type TonWalletReloadResult struct {
	Chain           string `json:"chain"`
	ReloadedWallets int    `json:"reloaded_wallets"`
	Error           string `json:"error,omitempty"`
}

type TonJettonReloadResult struct {
	Chain           string `json:"chain"`
	ReloadedJettons int    `json:"reloaded_jettons"`
	Error           string `json:"error,omitempty"`
}

// TonWalletReloader is implemented by TON workers that support runtime wallet reload.
type TonWalletReloader interface {
	Worker
	GetName() string
	GetNetworkType() enum.NetworkType
	ReloadWalletsFromKV(ctx context.Context) (int, error)
	ReloadWalletsFromDB(ctx context.Context) (int, error)
}

// TonJettonReloader is implemented by TON workers that support runtime jetton reload.
type TonJettonReloader interface {
	Worker
	GetName() string
	GetNetworkType() enum.NetworkType
	ReloadJettons(ctx context.Context) (int, error)
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
