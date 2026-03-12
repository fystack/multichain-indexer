package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"gorm.io/gorm"
)

var (
	ErrNoTonWorkerConfigured = errors.New("no ton worker configured")
	ErrTonWorkerNotFound     = errors.New("ton worker not found")
)

type WalletReloadSource string

const WalletReloadSourceDB WalletReloadSource = "db"

func (s WalletReloadSource) Normalize() WalletReloadSource {
	return WalletReloadSourceDB
}

func (s WalletReloadSource) IsValid() bool {
	return s == "" || s == WalletReloadSourceDB
}

type WalletReloadRequest struct {
	Source      WalletReloadSource
	TypeFilter  enum.NetworkType
	ChainFilter string
}

type WalletReloadResult struct {
	Type                 enum.NetworkType `json:"type"`
	Chain                string           `json:"chain,omitempty"`
	ReloadedWallets      int              `json:"reloaded_wallets"`
	ReloadedJettonWallet int              `json:"reloaded_jetton_wallets,omitempty"`
	Error                string           `json:"error,omitempty"`
}

type TonJettonReloadRequest struct {
	ChainFilter string
}

type TonJettonReloadResult struct {
	Chain                string `json:"chain"`
	ReloadedJettons      int    `json:"reloaded_jettons"`
	ReloadedJettonWallet int    `json:"reloaded_jetton_wallets"`
	Error                string `json:"error,omitempty"`
}

type WalletReloadService struct {
	db         *gorm.DB
	redis      infra.RedisClient
	addressBF  addressbloomfilter.WalletAddressBloomFilter
	tonTargets map[string]tonReloadTarget
}

type TonJettonReloadService struct {
	db         *gorm.DB
	redis      infra.RedisClient
	tonTargets map[string]tonReloadTarget
}

type tonReloadTarget struct {
	name   string
	cfg    config.ChainConfig
	indexr *indexer.TonIndexer
}

func NewWalletReloadServiceFromManager(
	m *Manager,
	db *gorm.DB,
	redis infra.RedisClient,
	addressBF addressbloomfilter.WalletAddressBloomFilter,
) *WalletReloadService {
	svc := &WalletReloadService{
		db:         db,
		redis:      redis,
		addressBF:  addressBF,
		tonTargets: make(map[string]tonReloadTarget),
	}
	if m != nil {
		svc.tonTargets = collectTonReloadTargets(m.Workers())
	}
	return svc
}

func NewTonJettonReloadServiceFromManager(
	m *Manager,
	db *gorm.DB,
	redis infra.RedisClient,
) *TonJettonReloadService {
	svc := &TonJettonReloadService{
		db:         db,
		redis:      redis,
		tonTargets: make(map[string]tonReloadTarget),
	}
	if m != nil {
		svc.tonTargets = collectTonReloadTargets(m.Workers())
	}
	return svc
}

func (s *WalletReloadService) ReloadWallets(ctx context.Context, req WalletReloadRequest) ([]WalletReloadResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database is not configured")
	}
	if s.addressBF == nil {
		return nil, fmt.Errorf("address bloom filter is not configured")
	}

	wallets, err := loadWalletAddressesByType(ctx, s.db, req.TypeFilter)
	if err != nil {
		return nil, fmt.Errorf("load wallets: %w", err)
	}

	s.addressBF.Clear(req.TypeFilter)
	s.addressBF.AddBatch(wallets, req.TypeFilter)

	if req.TypeFilter != enum.NetworkTypeTon {
		return []WalletReloadResult{{
			Type:            req.TypeFilter,
			ReloadedWallets: len(wallets),
		}}, nil
	}

	targets, err := filterTonTargets(s.tonTargets, req.ChainFilter)
	if err != nil {
		return nil, err
	}

	results := make([]WalletReloadResult, 0, len(targets))
	for _, target := range targets {
		item := WalletReloadResult{
			Type:            enum.NetworkTypeTon,
			Chain:           target.name,
			ReloadedWallets: len(wallets),
		}

		masters, loadErr := loadTONJettonMastersFromCache(ctx, s.redis, target.name, target.cfg)
		if loadErr != nil {
			item.Error = loadErr.Error()
		} else {
			jettonWallets := deriveTONJettonWallets(ctx, target.name, target.cfg, target.indexr.Client(), wallets, masters)
			item.ReloadedJettonWallet = target.indexr.ReplaceTrackedJettonWallets(jettonWallets)
		}

		results = append(results, item)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Chain < results[j].Chain })
	return results, nil
}

func (s *TonJettonReloadService) ReloadTonJettons(ctx context.Context, req TonJettonReloadRequest) ([]TonJettonReloadResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database is not configured")
	}

	owners, err := loadWalletAddressesByType(ctx, s.db, enum.NetworkTypeTon)
	if err != nil {
		return nil, fmt.Errorf("load ton wallets: %w", err)
	}

	targets, err := filterTonTargets(s.tonTargets, req.ChainFilter)
	if err != nil {
		return nil, err
	}

	results := make([]TonJettonReloadResult, 0, len(targets))
	for _, target := range targets {
		item := TonJettonReloadResult{Chain: target.name}

		masters, loadErr := loadTONJettonMastersFromCache(ctx, s.redis, target.name, target.cfg)
		if loadErr != nil {
			item.Error = loadErr.Error()
		} else {
			item.ReloadedJettons = len(masters)
			wallets := deriveTONJettonWallets(ctx, target.name, target.cfg, target.indexr.Client(), owners, masters)
			item.ReloadedJettonWallet = target.indexr.ReplaceTrackedJettonWallets(wallets)
		}

		results = append(results, item)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Chain < results[j].Chain })
	return results, nil
}

func ParseNetworkType(value string) enum.NetworkType {
	switch enum.NetworkType(strings.ToLower(strings.TrimSpace(value))) {
	case enum.NetworkTypeEVM,
		enum.NetworkTypeTron,
		enum.NetworkTypeBtc,
		enum.NetworkTypeSol,
		enum.NetworkTypeApt,
		enum.NetworkTypeSui,
		enum.NetworkTypeCosmos,
		enum.NetworkTypeTon:
		return enum.NetworkType(strings.ToLower(strings.TrimSpace(value)))
	default:
		return ""
	}
}

func collectTonReloadTargets(workers []Worker) map[string]tonReloadTarget {
	targets := make(map[string]tonReloadTarget)
	for _, w := range workers {
		target, ok := targetFromWorker(w)
		if !ok {
			continue
		}
		targets[target.name] = target
	}
	return targets
}

func targetFromWorker(w Worker) (tonReloadTarget, bool) {
	switch typed := w.(type) {
	case *RegularWorker:
		return targetFromBase(typed.BaseWorker)
	case *CatchupWorker:
		return targetFromBase(typed.BaseWorker)
	case *ManualWorker:
		return targetFromBase(typed.BaseWorker)
	case *RescannerWorker:
		return targetFromBase(typed.BaseWorker)
	default:
		return tonReloadTarget{}, false
	}
}

func targetFromBase(base *BaseWorker) (tonReloadTarget, bool) {
	if base == nil || base.chain == nil || base.chain.GetNetworkType() != enum.NetworkTypeTon {
		return tonReloadTarget{}, false
	}

	idxr, ok := base.chain.(*indexer.TonIndexer)
	if !ok {
		return tonReloadTarget{}, false
	}

	return tonReloadTarget{
		name:   strings.ToLower(base.chain.GetNetworkInternalCode()),
		cfg:    base.config,
		indexr: idxr,
	}, true
}

func filterTonTargets(targets map[string]tonReloadTarget, chainFilter string) ([]tonReloadTarget, error) {
	if len(targets) == 0 {
		return nil, ErrNoTonWorkerConfigured
	}
	if chainFilter != "" {
		target, ok := targets[strings.ToLower(strings.TrimSpace(chainFilter))]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrTonWorkerNotFound, chainFilter)
		}
		return []tonReloadTarget{target}, nil
	}

	out := make([]tonReloadTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, target)
	}
	return out, nil
}

func loadWalletAddressesByType(ctx context.Context, db *gorm.DB, networkType enum.NetworkType) ([]string, error) {
	repo := repository.NewRepository[model.WalletAddress](db)
	const limit = 1000
	offset := 0
	set := make(map[string]struct{})

	for {
		rows, err := repo.Find(ctx, repository.FindOptions{
			Where:  repository.WhereType{"type": networkType},
			Select: []string{"address"},
			Limit:  limit,
			Offset: uint(offset),
		})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			if row == nil {
				continue
			}
			address := strings.TrimSpace(row.Address)
			if address == "" {
				continue
			}
			set[address] = struct{}{}
		}

		offset += limit
	}

	out := make([]string, 0, len(set))
	for address := range set {
		out = append(out, address)
	}
	sort.Strings(out)
	return out, nil
}
