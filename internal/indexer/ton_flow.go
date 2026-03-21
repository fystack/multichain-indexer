package indexer

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

const (
	tonJettonExcessesOpcode         uint64 = 0xd53276db
	tonJettonBurnOpcode             uint64 = 0x595f07bc
	tonJettonBurnNotificationOpcode uint64 = 0x7bdd97de
	tonDeDustNativeSwapOpcode       uint64 = 0xea06185d
	tonDeDustJettonSwapOpcode       uint64 = 0xe3a0d482
	tonStonfiSwapV1Opcode           uint64 = 0x25938561
	tonStonfiSwapV2Opcode           uint64 = 0x6664de2a
)

type tonFlowEnvelope struct {
	queryID          uint64
	forwardPayload   *cell.Cell
	forwardTonAmount string
}

func (t *TonIndexer) correlateTONTransactions(in []types.Transaction) []types.Transaction {
	if len(in) <= 1 {
		return in
	}

	flows := make(map[string][]types.Transaction, len(in))
	out := make([]types.Transaction, 0, len(in))
	for _, tx := range in {
		flowID := tx.GetMetadataString("flow_id")
		if flowID == "" || strings.HasPrefix(flowID, "tx:") {
			out = append(out, tx)
			continue
		}
		flows[flowID] = append(flows[flowID], tx)
	}

	for _, grouped := range flows {
		out = append(out, t.correlateTONFlow(grouped)...)
	}

	return out
}

func (t *TonIndexer) correlateTONFlow(group []types.Transaction) []types.Transaction {
	if len(group) == 0 {
		return nil
	}

	slices.SortStableFunc(group, func(a, b types.Transaction) int {
		if a.Timestamp != b.Timestamp {
			return cmpUint64(a.Timestamp, b.Timestamp)
		}
		return strings.Compare(a.GetMetadataString("subtype"), b.GetMetadataString("subtype"))
	})

	if swap := correlateTONSwapFlow(group); len(swap) > 0 {
		return swap
	}
	if refund := correlateTONTokenRefundFlow(group); len(refund) > 0 {
		return refund
	}
	if transfer := correlateTONJettonTransferFlow(group); len(transfer) > 0 {
		return transfer
	}

	return group
}

func correlateTONSwapFlow(group []types.Transaction) []types.Transaction {
	var (
		swapRequest *types.Transaction
		swapOut     *types.Transaction
	)

	for i := range group {
		tx := &group[i]
		if tx.GetMetadataString("protocol") == "" {
			continue
		}
		subtype := tx.GetMetadataString("subtype")
		if subtype == "dedust_swap" || subtype == "stonfi_swap" {
			swapRequest = tx
			break
		}
	}
	if swapRequest == nil {
		return nil
	}

	for i := range group {
		tx := &group[i]
		if tx.TxHash == swapRequest.TxHash && tx.GetMetadataString("subtype") == swapRequest.GetMetadataString("subtype") {
			continue
		}
		if tx.ToAddress == "" || swapRequest.FromAddress == "" {
			continue
		}
		if tx.ToAddress != swapRequest.FromAddress {
			continue
		}
		if tx.AssetAddress == swapRequest.AssetAddress && tx.Amount == swapRequest.Amount {
			continue
		}
		if tx.Type != constant.TxTypeTokenTransfer && tx.Type != constant.TxTypeNativeTransfer {
			continue
		}
		swapOut = tx
		break
	}
	if swapOut == nil {
		return nil
	}

	inTx := *swapRequest

	outTx := *swapOut
	outTx.SetMetadataString("protocol", swapRequest.GetMetadataString("protocol"))
	if outTx.GetMetadataString("subtype") == "" {
		outTx.SetMetadataString("subtype", swapRequest.GetMetadataString("subtype"))
	}

	return []types.Transaction{inTx, outTx}
}

func correlateTONTokenRefundFlow(group []types.Transaction) []types.Transaction {
	var outbound *types.Transaction
	for i := range group {
		tx := &group[i]
		if tx.GetMetadataString("subtype") != "transfer" || tx.Type != constant.TxTypeTokenTransfer {
			continue
		}
		outbound = tx
		break
	}
	if outbound == nil {
		return nil
	}

	for i := range group {
		tx := &group[i]
		if tx.TxHash == outbound.TxHash && tx.GetMetadataString("subtype") == outbound.GetMetadataString("subtype") {
			continue
		}
		if tx.Type != constant.TxTypeTokenTransfer {
			continue
		}
		if tx.ToAddress != outbound.FromAddress {
			continue
		}
		if tx.AssetAddress != outbound.AssetAddress {
			continue
		}
		refund := *tx
		refund.SetMetadataString("subtype", "refund")
		refund.SetMetadataString("related_address", outbound.ToAddress)
		refund.SetMetadataString("protocol", outbound.GetMetadataString("protocol"))
		return []types.Transaction{refund}
	}
	return nil
}

func correlateTONJettonTransferFlow(group []types.Transaction) []types.Transaction {
	var (
		transfer     *types.Transaction
		notification *types.Transaction
		internal     *types.Transaction
		others       []types.Transaction
	)

	for i := range group {
		tx := &group[i]
		switch tx.GetMetadataString("subtype") {
		case "transfer":
			if transfer == nil {
				transfer = tx
				continue
			}
		case "transfer_notification":
			if notification == nil {
				notification = tx
				continue
			}
		case "internal_transfer":
			if internal == nil {
				internal = tx
				continue
			}
		}
		others = append(others, *tx)
	}

	var primary *types.Transaction
	switch {
	case transfer != nil:
		primary = transfer
	case notification != nil:
		primary = notification
	case internal != nil:
		primary = internal
	default:
		return nil
	}

	out := []types.Transaction{*primary}
	out = append(out, others...)
	return out
}

func buildTONFlowID(queryID uint64, tx types.Transaction) string {
	subtype := tx.GetMetadataString("subtype")
	protocol := tx.GetMetadataString("protocol")
	if queryID == 0 {
		return fmt.Sprintf("tx:%s:%s", tx.TxHash, subtype)
	}

	parts := []string{
		strconv.FormatUint(queryID, 10),
		string(tx.Type),
		tx.AssetAddress,
		tx.FromAddress,
		tx.ToAddress,
		protocol,
	}
	return "q:" + strings.Join(parts, "|")
}

func classifyTONJettonProtocol(payload *cell.Cell) (protocol string, subtype string) {
	if payload == nil {
		return "", ""
	}

	opcode, ok := messageCellOpcode(payload)
	if !ok {
		return "", ""
	}

	switch opcode {
	case tonDeDustNativeSwapOpcode, tonDeDustJettonSwapOpcode:
		return "dedust", "dedust_swap"
	case tonStonfiSwapV1Opcode, tonStonfiSwapV2Opcode:
		return "stonfi", "stonfi_swap"
	default:
		return "", ""
	}
}

func messageCellOpcode(c *cell.Cell) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	bodySlice := c.BeginParse()
	if bodySlice.BitsLeft() < 32 {
		return 0, false
	}

	opcode, err := bodySlice.LoadUInt(32)
	if err != nil {
		return 0, false
	}
	return opcode, true
}

func parseJettonTransferEnvelope(msg *tlb.InternalMessage) (amount string, destination *address.Address, responseDestination *address.Address, env tonFlowEnvelope, ok bool) {
	if msg == nil || msg.Body == nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil { // opcode
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	queryID, err := bodySlice.LoadUInt(64)
	if err != nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	destination, err = bodySlice.LoadAddr()
	if err != nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}
	responseDestination, err = bodySlice.LoadAddr()
	if err != nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	if _, err = bodySlice.LoadMaybeRef(); err != nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	forwardTonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return "", nil, nil, tonFlowEnvelope{}, false
	}

	var forwardPayload *cell.Cell
	isRef, err := bodySlice.LoadBoolBit()
	if err == nil {
		if isRef {
			forwardPayload, err = bodySlice.LoadRefCell()
		} else {
			forwardPayload, err = bodySlice.Copy().ToCell()
		}
		if err != nil {
			forwardPayload = nil
		}
	}

	return jettonAmount.String(), destination, responseDestination, tonFlowEnvelope{
		queryID:          queryID,
		forwardPayload:   forwardPayload,
		forwardTonAmount: forwardTonAmount.String(),
	}, true
}

func parseJettonInternalTransferBody(msg *tlb.InternalMessage) (queryID uint64, amount string, from *address.Address, responseDestination *address.Address, ok bool) {
	if msg == nil || msg.Body == nil {
		return 0, "", nil, nil, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil {
		return 0, "", nil, nil, false
	}
	queryID, err := bodySlice.LoadUInt(64)
	if err != nil {
		return 0, "", nil, nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return 0, "", nil, nil, false
	}

	from, err = bodySlice.LoadAddr()
	if err != nil {
		return 0, "", nil, nil, false
	}
	responseDestination, err = bodySlice.LoadAddr()
	if err != nil {
		return 0, "", nil, nil, false
	}

	return queryID, jettonAmount.String(), from, responseDestination, true
}

func parseJettonTransferNotificationBody(msg *tlb.InternalMessage) (queryID uint64, amount string, sender *address.Address, ok bool) {
	if msg == nil || msg.Body == nil {
		return 0, "", nil, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil {
		return 0, "", nil, false
	}
	queryID, err := bodySlice.LoadUInt(64)
	if err != nil {
		return 0, "", nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return 0, "", nil, false
	}

	sender, err = bodySlice.LoadAddr()
	if err != nil {
		return 0, "", nil, false
	}

	return queryID, jettonAmount.String(), sender, true
}

func parseJettonExcessesBody(msg *tlb.InternalMessage) (uint64, bool) {
	if msg == nil || msg.Body == nil {
		return 0, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil {
		return 0, false
	}
	queryID, err := bodySlice.LoadUInt(64)
	if err != nil {
		return 0, false
	}
	return queryID, true
}

func parseJettonBurnBody(msg *tlb.InternalMessage) (queryID uint64, amount string, responseDestination *address.Address, ok bool) {
	if msg == nil || msg.Body == nil {
		return 0, "", nil, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil {
		return 0, "", nil, false
	}
	queryID, err := bodySlice.LoadUInt(64)
	if err != nil {
		return 0, "", nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return 0, "", nil, false
	}

	responseDestination, err = bodySlice.LoadAddr()
	if err != nil {
		return 0, "", nil, false
	}

	return queryID, jettonAmount.String(), responseDestination, true
}

func parseJettonBurnNotificationBody(msg *tlb.InternalMessage) (queryID uint64, amount string, owner *address.Address, responseDestination *address.Address, ok bool) {
	if msg == nil || msg.Body == nil {
		return 0, "", nil, nil, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil {
		return 0, "", nil, nil, false
	}
	queryID, err := bodySlice.LoadUInt(64)
	if err != nil {
		return 0, "", nil, nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return 0, "", nil, nil, false
	}

	owner, err = bodySlice.LoadAddr()
	if err != nil {
		return 0, "", nil, nil, false
	}
	responseDestination, err = bodySlice.LoadAddr()
	if err != nil {
		return 0, "", nil, nil, false
	}
	return queryID, jettonAmount.String(), owner, responseDestination, true
}

func nativeTONAmount(msg *tlb.InternalMessage) string {
	if msg == nil {
		return ""
	}
	return msg.Amount.Nano().String()
}

func txFeeDecimal(tx *tlb.Transaction) decimal.Decimal {
	if tx == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1_000_000_000))
}

func cmpUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
