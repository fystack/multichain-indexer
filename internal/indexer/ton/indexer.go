package ton

import (
	"context"
	"encoding/hex"
	"fmt"

	tonRpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
)

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
	// Parse the address
	addr, err := address.ParseAddr(addrStr)
	if err != nil {
		return nil, cursor, fmt.Errorf("invalid TON address %s: %w", addrStr, err)
	}

	// Prepare cursor values for API call
	var lastLT uint64
	var lastHash []byte
	if cursor != nil && cursor.LastLT > 0 {
		lastLT = cursor.LastLT
		if cursor.LastHash != "" {
			lastHash, err = hex.DecodeString(cursor.LastHash)
			if err != nil {
				return nil, cursor, fmt.Errorf("invalid cursor hash: %w", err)
			}
		}
	}

	// Fetch transactions from the account
	// The client handles failover and retries internally
	txs, err := i.client.ListTransactions(ctx, addr, i.txLimit, lastLT, lastHash)
	if err != nil {
		return nil, cursor, fmt.Errorf("failed to list transactions: %w", err)
	}

	// No new transactions
	if len(txs) == 0 {
		return nil, cursor, nil
	}

	// Process transactions in reverse order (oldest first for proper cursor updates)
	// TON API returns newest first, so we iterate backwards
	var parsedTxs []types.Transaction
	newCursor := cursor
	if newCursor == nil {
		newCursor = &AccountCursor{Address: addrStr}
	}

	// Normalize address string for consistent comparison
	addrStr = addr.String()

	for j := len(txs) - 1; j >= 0; j-- {
		tx := txs[j]

		// Skip transactions we've already processed (at or before cursor)
		if cursor != nil && tx.LT <= cursor.LastLT {
			continue
		}

		// Update cursor to this transaction (even if it's skipped for failure)
		newCursor.LastLT = tx.LT
		newCursor.LastHash = hex.EncodeToString(tx.Hash)

		// Check if transaction was successful
		if !isTransactionSuccess(tx) {
			continue
		}

		var collectedTxs []types.Transaction
		collectedTxs = append(collectedTxs, ParseTonTransfer(tx, addrStr, i.config.InternalCode)...)

		if i.jettonRegistry != nil {
			collectedTxs = append(collectedTxs, ParseJettonTransfer(tx, addrStr, i.config.InternalCode, i.jettonRegistry)...)
		}

		parsedTxs = append(parsedTxs, collectedTxs...)
	}
	return parsedTxs, newCursor, nil
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
