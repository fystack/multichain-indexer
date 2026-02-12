package ton

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"

	tonRpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/infra"
	tonCursorStore "github.com/fystack/multichain-indexer/pkg/store/toncursorstore"
	"github.com/xssnick/tonutils-go/tlb"
)

type AccountCursor = tonCursorStore.AccountCursor
type CursorStore = tonCursorStore.Store

func NewCursorStore(kv infra.KVStore) CursorStore {
	return tonCursorStore.New(kv)
}

// AccountIndexer is the interface for account-based indexing (TON).
// This is separate from the block-based Indexer interface used by EVM/Solana/etc.
type AccountIndexer interface {
	GetName() string
	GetNetworkType() enum.NetworkType
	GetNetworkInternalCode() string

	// PollAccount fetches new transactions for a single account.
	// Returns parsed transactions and the new cursor position.
	// If no new transactions, returns empty slice and same cursor.
	PollAccount(ctx context.Context, address string, cursor *AccountCursor) ([]types.Transaction, *AccountCursor, error)

	// IsHealthy checks if the RPC connection is healthy.
	IsHealthy() bool

	// ReloadJettons refreshes supported jetton metadata at runtime.
	ReloadJettons(ctx context.Context) (int, error)
}

// TonAccountIndexer implements AccountIndexer for TON blockchain.
type TonAccountIndexer struct {
	chainName      string
	config         config.ChainConfig
	client         tonRpc.TonAPI
	jettonRegistry JettonRegistry
	jettonMasterMu sync.RWMutex
	jettonMasters  map[string]string // jetton wallet -> jetton master

	// Transaction limit per poll
	txLimit uint32
}

// NewTonAccountIndexer creates a new TON account indexer.
func NewTonAccountIndexer(
	chainName string,
	cfg config.ChainConfig,
	client tonRpc.TonAPI,
	jettonRegistry JettonRegistry,
) *TonAccountIndexer {
	txLimit := uint32(50) // Default transaction limit
	if cfg.Throttle.BatchSize > 0 {
		txLimit = uint32(cfg.Throttle.BatchSize)
	}

	return &TonAccountIndexer{
		chainName:      chainName,
		config:         cfg,
		client:         client,
		jettonRegistry: jettonRegistry,
		jettonMasters:  make(map[string]string),
		txLimit:        txLimit,
	}
}

func (i *TonAccountIndexer) GetName() string                  { return i.chainName }
func (i *TonAccountIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeTon }
func (i *TonAccountIndexer) GetNetworkInternalCode() string   { return i.config.InternalCode }

// IsHealthy checks if the RPC connection is healthy.
func (i *TonAccountIndexer) IsHealthy() bool {
	// The client manages its own connection pool and recovery.
	// We consider it healthy if it's initialized.
	return i.client != nil
}

func (i *TonAccountIndexer) ReloadJettons(ctx context.Context) (int, error) {
	if i.jettonRegistry == nil {
		return 0, nil
	}

	type registryReloader interface {
		Reload(context.Context) error
	}

	if reloader, ok := i.jettonRegistry.(registryReloader); ok {
		if err := reloader.Reload(ctx); err != nil {
			return 0, fmt.Errorf("reload jetton registry: %w", err)
		}
	}

	return len(i.jettonRegistry.List()), nil
}

// PollAccount fetches new transactions for a single account.
func (i *TonAccountIndexer) PollAccount(ctx context.Context, addrStr string, cursor *AccountCursor) ([]types.Transaction, *AccountCursor, error) {
	if i.client == nil {
		return nil, cursor, fmt.Errorf("ton rpc client is not initialized")
	}

	addr, normalizedAddress, err := resolvePollAddress(addrStr)
	if err != nil {
		return nil, cursor, err
	}

	lastLT, lastHash, err := decodeCursorForRPC(cursor)
	if err != nil {
		return nil, cursor, err
	}

	txs, err := i.client.ListTransactions(ctx, addr, i.txLimit, lastLT, lastHash)
	if err != nil {
		return nil, cursor, fmt.Errorf("failed to list transactions: %w", err)
	}

	// No new transactions
	if len(txs) == 0 {
		return nil, cursor, nil
	}

	parsedTxs := make([]types.Transaction, 0, len(txs))
	newCursor := ensureCursor(cursor, addrStr)
	var latestSeqno uint64
	var latestSeqnoFetched bool

	// TON API returns newest first, so process backwards (oldest to newest).
	for j := len(txs) - 1; j >= 0; j-- {
		tx := txs[j]

		if isAlreadyProcessed(cursor, tx) {
			continue
		}

		advanceCursor(newCursor, tx)

		if !isTransactionSuccess(tx) {
			continue
		}

		matchedTxs := i.parseMatchedTransactions(tx, normalizedAddress)
		if len(matchedTxs) == 0 {
			continue
		}
		i.resolveJettonAssetAddresses(ctx, matchedTxs)

		if !latestSeqnoFetched {
			latestSeqno, err = i.client.GetLatestMasterchainSeqno(ctx)
			if err != nil {
				return nil, cursor, fmt.Errorf("failed to get latest masterchain seqno: %w", err)
			}
			latestSeqnoFetched = true
		}

		for idx := range matchedTxs {
			matchedTxs[idx].BlockNumber = latestSeqno
			matchedTxs[idx].MasterchainSeqno = latestSeqno
		}

		parsedTxs = append(parsedTxs, matchedTxs...)
	}

	return parsedTxs, newCursor, nil
}

func (i *TonAccountIndexer) resolveJettonAssetAddresses(ctx context.Context, txs []types.Transaction) {
	for idx := range txs {
		tx := &txs[idx]
		if tx.Type != constant.TxTypeTokenTransfer || tx.AssetAddress == "" {
			continue
		}

		walletAddress := tx.AssetAddress
		if i.jettonRegistry != nil {
			if info, ok := i.jettonRegistry.GetInfo(walletAddress); ok {
				tx.AssetAddress = info.MasterAddress
				continue
			}
			if info, ok := i.jettonRegistry.GetInfoByWallet(walletAddress); ok {
				tx.AssetAddress = info.MasterAddress
				i.cacheJettonMaster(walletAddress, info.MasterAddress)
				continue
			}
		}

		if cachedMaster, ok := i.getCachedJettonMaster(walletAddress); ok {
			tx.AssetAddress = cachedMaster
			continue
		}

		resolvedMaster, err := i.client.ResolveJettonMasterAddress(ctx, walletAddress)
		if err != nil || resolvedMaster == "" {
			continue
		}

		i.cacheJettonMaster(walletAddress, resolvedMaster)
		tx.AssetAddress = resolvedMaster
		if i.jettonRegistry != nil {
			i.jettonRegistry.RegisterWallet(walletAddress, resolvedMaster)
		}
	}
}

func (i *TonAccountIndexer) getCachedJettonMaster(walletAddress string) (string, bool) {
	i.jettonMasterMu.RLock()
	defer i.jettonMasterMu.RUnlock()
	master, ok := i.jettonMasters[walletAddress]
	return master, ok
}

func (i *TonAccountIndexer) cacheJettonMaster(walletAddress, masterAddress string) {
	i.jettonMasterMu.Lock()
	defer i.jettonMasterMu.Unlock()
	i.jettonMasters[walletAddress] = masterAddress
}

func decodeCursorForRPC(cursor *AccountCursor) (uint64, []byte, error) {
	if cursor == nil || cursor.LastLT == 0 {
		return 0, nil, nil
	}

	lastLT := cursor.LastLT
	if cursor.LastHash == "" {
		return lastLT, nil, nil
	}

	lastHash, err := hex.DecodeString(cursor.LastHash)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid cursor hash: %w", err)
	}

	return lastLT, lastHash, nil
}

func ensureCursor(cursor *AccountCursor, address string) *AccountCursor {
	if cursor != nil {
		return cursor
	}
	return &AccountCursor{Address: address}
}

func isAlreadyProcessed(cursor *AccountCursor, tx *tlb.Transaction) bool {
	return cursor != nil && tx.LT <= cursor.LastLT
}

func advanceCursor(cursor *AccountCursor, tx *tlb.Transaction) {
	cursor.LastLT = tx.LT
	cursor.LastHash = hex.EncodeToString(tx.Hash)
}

// isTransactionSuccess checks if the transaction was successful by examining its phases.
func isTransactionSuccess(tx *tlb.Transaction) bool {
	if tx.Description == nil {
		return true // Should not happen with valid transactions
	}

	desc, ok := tx.Description.(*tlb.TransactionDescriptionOrdinary)
	if !ok {
		return true
	}

	// Check if the whole transaction was aborted
	if desc.Aborted {
		return false
	}

	// Check Compute Phase
	if desc.ComputePhase.Phase != nil {
		if cp, ok := desc.ComputePhase.Phase.(*tlb.ComputePhaseVM); ok {
			if !cp.Success {
				return false
			}
		}
	}

	// Check Action Phase
	if desc.ActionPhase != nil {
		if !desc.ActionPhase.Success {
			return false
		}
	}

	return true
}

// JettonInfo describes a supported Jetton token.
type JettonInfo struct {
	MasterAddress string `json:"master_address" yaml:"master_address"` // Jetton master contract
	Symbol        string `json:"symbol"         yaml:"symbol"`
	Decimals      int    `json:"decimals"       yaml:"decimals"`
}

// JettonRegistry manages supported Jetton tokens.
type JettonRegistry interface {
	// IsSupported checks if a Jetton wallet belongs to a supported Jetton.
	// This may require looking up the wallet's master address.
	IsSupported(walletAddress string) bool

	// GetInfo returns info for a Jetton by its master address.
	GetInfo(masterAddress string) (*JettonInfo, bool)

	// GetInfoByWallet returns info for a Jetton by a wallet address.
	// Returns nil if the wallet is not from a known Jetton.
	GetInfoByWallet(walletAddress string) (*JettonInfo, bool)

	// RegisterWallet associates a Jetton wallet with its master address.
	RegisterWallet(walletAddress, masterAddress string)

	// List returns all supported Jettons.
	List() []JettonInfo
}
