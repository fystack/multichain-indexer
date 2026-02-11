package ton

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tonRpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/shopspring/decimal"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
)

const (
	cursorKeyPrefix = "ton/cursor/"

	// Jetton standard opcodes (TEP-74)
	OpcodeTransfer             uint64 = 0x0f8a7ea5
	OpcodeTransferNotification uint64 = 0x7362d09c
)

// AccountCursor tracks the polling position for a single TON account.
type AccountCursor struct {
	Address   string    `json:"address"`
	LastLT    uint64    `json:"last_lt"`   // Logical time of last processed tx
	LastHash  string    `json:"last_hash"` // Hex-encoded hash of last processed tx
	UpdatedAt time.Time `json:"updated_at"`
}

// CursorStore manages account cursors for TON polling.
type CursorStore interface {
	// Get returns the cursor for an account, or nil if not found.
	Get(ctx context.Context, address string) (*AccountCursor, error)

	// Save persists the cursor atomically.
	Save(ctx context.Context, cursor *AccountCursor) error

	// Delete removes the cursor for an account.
	Delete(ctx context.Context, address string) error

	// List returns all tracked account addresses.
	List(ctx context.Context) ([]string, error)
}

// kvCursorStore implements CursorStore using infra.KVStore.
type kvCursorStore struct {
	kv infra.KVStore
}

func NewCursorStore(kv infra.KVStore) CursorStore {
	return &kvCursorStore{kv: kv}
}

func (s *kvCursorStore) cursorKey(address string) string {
	return cursorKeyPrefix + address
}

func (s *kvCursorStore) Get(ctx context.Context, address string) (*AccountCursor, error) {
	var cursor AccountCursor
	found, err := s.kv.GetAny(s.cursorKey(address), &cursor)
	if err != nil {
		return nil, fmt.Errorf("failed to get cursor for %s: %w", address, err)
	}
	if !found {
		return nil, nil
	}
	return &cursor, nil
}

func (s *kvCursorStore) Save(ctx context.Context, cursor *AccountCursor) error {
	cursor.UpdatedAt = time.Now()
	if err := s.kv.SetAny(s.cursorKey(cursor.Address), cursor); err != nil {
		return fmt.Errorf("failed to save cursor for %s: %w", cursor.Address, err)
	}
	return nil
}

func (s *kvCursorStore) Delete(ctx context.Context, address string) error {
	if err := s.kv.Delete(s.cursorKey(address)); err != nil {
		return fmt.Errorf("failed to delete cursor for %s: %w", address, err)
	}
	return nil
}

func (s *kvCursorStore) List(ctx context.Context) ([]string, error) {
	pairs, err := s.kv.List(cursorKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list cursors: %w", err)
	}

	addresses := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		var cursor AccountCursor
		if err := json.Unmarshal(pair.Value, &cursor); err != nil {
			continue // Skip malformed entries
		}
		addresses = append(addresses, cursor.Address)
	}

	return addresses, nil
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

		parsedTxs = append(parsedTxs, i.parseMatchedTransactions(tx, normalizedAddress)...)
	}

	return parsedTxs, newCursor, nil
}

func resolvePollAddress(addrStr string) (*address.Address, string, error) {
	addr, err := parseTONAddress(addrStr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid TON address %s: %w", addrStr, err)
	}
	return addr, addr.StringRaw(), nil
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

func (i *TonAccountIndexer) parseMatchedTransactions(
	tx *tlb.Transaction,
	normalizedAddress string,
) []types.Transaction {
	collected := parseTonTransferNormalized(tx, normalizedAddress, i.config.InternalCode)
	if i.jettonRegistry != nil {
		collected = append(collected, parseJettonTransferNormalized(tx, normalizedAddress, i.config.InternalCode, i.jettonRegistry)...)
	}
	return collected
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

// NormalizeTONAddressRaw returns canonical raw format (workchain:hex) or empty if invalid.
func NormalizeTONAddressRaw(addr string) string {
	parsed, err := parseTONAddress(addr)
	if err != nil {
		return ""
	}
	return parsed.StringRaw()
}

// NormalizeTONAddressList trims and de-duplicates addresses while preserving order.
func NormalizeTONAddressList(addresses []string) []string {
	if len(addresses) == 0 {
		return nil
	}

	dedup := make(map[string]struct{}, len(addresses))
	result := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if _, exists := dedup[addr]; exists {
			continue
		}
		dedup[addr] = struct{}{}
		result = append(result, addr)
	}

	return result
}

func parseTONAddress(addrStr string) (*address.Address, error) {
	addrStr = strings.TrimSpace(addrStr)

	// User-friendly format (base64url with checksum), e.g. EQ...
	if addr, err := address.ParseAddr(addrStr); err == nil {
		return addr, nil
	}
	// Raw format, e.g. 0:abcdef...
	if addr, err := address.ParseRawAddr(addrStr); err == nil {
		return addr, nil
	}

	// Defensive normalization for malformed historical values.
	parts := strings.SplitN(addrStr, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid address format")
	}

	rawHex := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(parts[1])), "0x")
	if rawHex == "" {
		return nil, fmt.Errorf("empty address payload")
	}

	if len(rawHex)%2 == 1 {
		rawHex = "0" + rawHex
	}
	if len(rawHex) > 64 {
		rawHex = rawHex[len(rawHex)-64:]
	} else if len(rawHex) < 64 {
		rawHex = strings.Repeat("0", 64-len(rawHex)) + rawHex
	}

	return address.ParseRawAddr(parts[0] + ":" + rawHex)
}

// ParseTonTransfer extracts native TON transfers from a transaction.
// Detects both incoming (receive) and outgoing (send) transfers.
func ParseTonTransfer(tx *tlb.Transaction, ourAddress string, networkID string) []types.Transaction {
	normalizedOurAddress := NormalizeTONAddressRaw(ourAddress)
	if normalizedOurAddress == "" {
		return nil
	}
	return parseTonTransferNormalized(tx, normalizedOurAddress, networkID)
}

func parseTonTransferNormalized(tx *tlb.Transaction, normalizedOurAddress string, networkID string) []types.Transaction {
	var results []types.Transaction

	if parsed, ok := parseIncomingTransfer(tx, normalizedOurAddress, networkID); ok {
		results = append(results, *parsed)
	}

	for _, intMsg := range outgoingInternalMessages(tx) {
		parsed, ok := parseOutgoingTransferMessage(tx, intMsg, normalizedOurAddress, networkID)
		if !ok {
			continue
		}
		results = append(results, parsed)
	}

	return results
}

func parseIncomingTransfer(tx *tlb.Transaction, ourAddress string, networkID string) (*types.Transaction, bool) {
	intMsg, ok := incomingInternalMessage(tx)
	if !ok {
		return nil, false
	}

	dstAddrRaw := intMsg.DstAddr.StringRaw()
	if dstAddrRaw != ourAddress || !isSimpleTransferMessage(intMsg) {
		return nil, false
	}

	amount := intMsg.Amount.Nano().String()
	if amount == "0" {
		return nil, false
	}

	txData := newBaseParsedTransaction(tx, networkID)
	txData.FromAddress = intMsg.SrcAddr.StringRaw()
	txData.ToAddress = dstAddrRaw
	txData.AssetAddress = ""
	txData.Amount = amount
	txData.Type = constant.TxTypeNativeTransfer
	return &txData, true
}

func parseOutgoingTransferMessage(
	tx *tlb.Transaction,
	intMsg *tlb.InternalMessage,
	ourAddress string,
	networkID string,
) (types.Transaction, bool) {
	srcAddr := intMsg.SrcAddr.StringRaw()
	if srcAddr != ourAddress || !isSimpleTransferMessage(intMsg) {
		return types.Transaction{}, false
	}

	amount := intMsg.Amount.Nano().String()
	if amount == "0" {
		return types.Transaction{}, false
	}

	txData := newBaseParsedTransaction(tx, networkID)
	txData.FromAddress = srcAddr
	txData.ToAddress = intMsg.DstAddr.StringRaw()
	txData.AssetAddress = ""
	txData.Amount = amount
	txData.Type = constant.TxTypeNativeTransfer
	return txData, true
}

// ParseJettonTransfer extracts Jetton transfers from a transaction.
func ParseJettonTransfer(tx *tlb.Transaction, ourAddress string, networkID string, registry JettonRegistry) []types.Transaction {
	normalizedOurAddress := NormalizeTONAddressRaw(ourAddress)
	if normalizedOurAddress == "" {
		return nil
	}
	return parseJettonTransferNormalized(tx, normalizedOurAddress, networkID, registry)
}

func parseJettonTransferNormalized(
	tx *tlb.Transaction,
	normalizedOurAddress string,
	networkID string,
	registry JettonRegistry,
) []types.Transaction {
	var results []types.Transaction

	if parsed, ok := parseIncomingJetton(tx, normalizedOurAddress, networkID, registry); ok {
		results = append(results, *parsed)
	}

	for _, intMsg := range outgoingInternalMessages(tx) {
		parsed, ok := parseOutgoingJettonMessage(tx, intMsg, normalizedOurAddress, networkID, registry)
		if !ok {
			continue
		}
		results = append(results, parsed)
	}

	return results
}

func parseIncomingJetton(tx *tlb.Transaction, ourAddress string, networkID string, registry JettonRegistry) (*types.Transaction, bool) {
	intMsg, ok := incomingInternalMessage(tx)
	if !ok {
		return nil, false
	}

	dstAddrRaw := intMsg.DstAddr.StringRaw()
	if dstAddrRaw != ourAddress || !messageHasOpcode(intMsg, OpcodeTransferNotification) {
		return nil, false
	}

	jettonAmount, sender, ok := parseJettonTransferBody(intMsg)
	if !ok || sender == nil {
		return nil, false
	}

	jettonWallet := intMsg.SrcAddr.StringRaw()

	txData := newBaseParsedTransaction(tx, networkID)
	txData.FromAddress = sender.StringRaw()
	txData.ToAddress = dstAddrRaw
	txData.AssetAddress = resolveJettonAssetAddress(registry, jettonWallet)
	txData.Amount = jettonAmount
	txData.Type = constant.TxTypeTokenTransfer
	return &txData, true
}

func parseOutgoingJettonMessage(
	tx *tlb.Transaction,
	intMsg *tlb.InternalMessage,
	ourAddress string,
	networkID string,
	registry JettonRegistry,
) (types.Transaction, bool) {
	if !messageHasOpcode(intMsg, OpcodeTransfer) {
		return types.Transaction{}, false
	}

	jettonAmount, destination, ok := parseJettonTransferBody(intMsg)
	if !ok || destination == nil {
		return types.Transaction{}, false
	}

	jettonWallet := intMsg.DstAddr.StringRaw()

	txData := newBaseParsedTransaction(tx, networkID)
	txData.FromAddress = ourAddress
	txData.ToAddress = destination.StringRaw()
	txData.AssetAddress = resolveJettonAssetAddress(registry, jettonWallet)
	txData.Amount = jettonAmount
	txData.Type = constant.TxTypeTokenTransfer
	return txData, true
}

func parseJettonTransferBody(msg *tlb.InternalMessage) (string, *address.Address, bool) {
	if msg.Body == nil {
		return "", nil, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil { // opcode
		return "", nil, false
	}
	if _, err := bodySlice.LoadUInt(64); err != nil { // query_id
		return "", nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return "", nil, false
	}

	peerAddress, err := bodySlice.LoadAddr()
	if err != nil {
		return "", nil, false
	}

	return jettonAmount.String(), peerAddress, true
}

func incomingInternalMessage(tx *tlb.Transaction) (*tlb.InternalMessage, bool) {
	if tx.IO.In == nil {
		return nil, false
	}

	intMsg, ok := tx.IO.In.Msg.(*tlb.InternalMessage)
	if !ok || intMsg.Bounced {
		return nil, false
	}

	return intMsg, true
}

func outgoingInternalMessages(tx *tlb.Transaction) []*tlb.InternalMessage {
	if tx.IO.Out == nil {
		return nil
	}

	outList, err := tx.IO.Out.ToSlice()
	if err != nil {
		return nil
	}

	messages := make([]*tlb.InternalMessage, 0, len(outList))
	for _, outMsg := range outList {
		intMsg, ok := outMsg.Msg.(*tlb.InternalMessage)
		if !ok {
			continue
		}
		messages = append(messages, intMsg)
	}
	return messages
}

func messageOpcode(msg *tlb.InternalMessage) (uint64, bool) {
	if msg.Body == nil {
		return 0, false
	}

	bodySlice := msg.Body.BeginParse()
	if bodySlice.BitsLeft() < 32 {
		return 0, false
	}

	opcode, err := bodySlice.LoadUInt(32)
	if err != nil {
		return 0, false
	}
	return opcode, true
}

func isSimpleTransferMessage(msg *tlb.InternalMessage) bool {
	if msg.Body == nil {
		return true
	}

	opcode, ok := messageOpcode(msg)
	if !ok {
		return true
	}

	// Opcode 0 means comment payload for regular TON transfer.
	return opcode == 0
}

func messageHasOpcode(msg *tlb.InternalMessage, expected uint64) bool {
	opcode, ok := messageOpcode(msg)
	return ok && opcode == expected
}

func resolveJettonAssetAddress(registry JettonRegistry, jettonWallet string) string {
	if registry == nil {
		return jettonWallet
	}
	if info, ok := registry.GetInfoByWallet(jettonWallet); ok {
		return info.MasterAddress
	}
	return jettonWallet
}

func newBaseParsedTransaction(tx *tlb.Transaction, networkID string) types.Transaction {
	return types.Transaction{
		TxHash:      base64.StdEncoding.EncodeToString(tx.Hash),
		NetworkId:   networkID,
		BlockNumber: 0,
		LogicalTime: tx.LT,
		TxFee:       decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1e9)),
		Timestamp:   uint64(tx.Now),
		Status:      types.StatusConfirmed,
	}
}
