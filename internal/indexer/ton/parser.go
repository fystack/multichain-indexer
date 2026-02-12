package ton

import (
	"encoding/hex"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
)

const (
	// Jetton standard opcodes (TEP-74)
	OpcodeTransfer             uint64 = 0x0f8a7ea5
	OpcodeTransferNotification uint64 = 0x7362d09c
)

func (i *TonAccountIndexer) transactionNetworkID() string {
	return strings.TrimSpace(i.config.NetworkId)
}

func (i *TonAccountIndexer) parseMatchedTransactions(
	tx *tlb.Transaction,
	normalizedAddress string,
) []types.Transaction {
	networkID := i.transactionNetworkID()
	collected := parseTonTransferNormalized(tx, normalizedAddress, networkID)
	if i.jettonRegistry != nil {
		collected = append(collected, parseJettonTransferNormalized(tx, normalizedAddress, networkID, i.jettonRegistry)...)
	}
	return collected
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

func encodeTONTxHash(hash []byte) string {
	return hex.EncodeToString(hash)
}

func newBaseParsedTransaction(tx *tlb.Transaction, networkID string) types.Transaction {
	return types.Transaction{
		TxHash:      encodeTONTxHash(tx.Hash),
		NetworkId:   networkID,
		BlockNumber: 0,
		LogicalTime: tx.LT,
		TxFee:       decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1e9)),
		Timestamp:   uint64(tx.Now),
		Status:      types.StatusConfirmed,
	}
}
