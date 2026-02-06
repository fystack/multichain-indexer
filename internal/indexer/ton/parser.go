package ton

import (
	"encoding/base64"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
	"github.com/xssnick/tonutils-go/tlb"
)

// ParseTonTransfer extracts native TON transfers from a transaction.
// Detects both incoming (receive) and outgoing (send) transfers.
// Returns a slice of parsed transactions involving our address.
func ParseTonTransfer(tx *tlb.Transaction, ourAddress string, networkID string) []types.Transaction {
	var results []types.Transaction

	// Try to parse as incoming transfer
	if parsed, ok := parseIncomingTransfer(tx, ourAddress, networkID); ok {
		results = append(results, *parsed)
	}

	// Try to parse as outgoing transfers (can be multiple)
	results = append(results, parseOutgoingTransfers(tx, ourAddress, networkID)...)

	return results
}

// parseIncomingTransfer handles incoming TON transfers (receive).
func parseIncomingTransfer(tx *tlb.Transaction, ourAddress string, networkID string) (*types.Transaction, bool) {
	// Check for incoming internal message
	if tx.IO.In == nil {
		return nil, false
	}

	// Only process internal messages (wallet-to-wallet transfers)
	inMsg := tx.IO.In.Msg
	intMsg, ok := inMsg.(*tlb.InternalMessage)
	if !ok || intMsg.Bounced {
		return nil, false
	}

	// Verify destination is our account
	dstAddr := intMsg.DstAddr.String()
	if dstAddr != ourAddress {
		return nil, false
	}

	// Extract amount (in nanoTON)
	amount := intMsg.Amount.Nano().String()

	// Skip zero-value messages (typically these are just notifications)
	if amount == "0" {
		return nil, false
	}

	// IF the message has a body, check if it's a simple comment (opcode 0)
	// If it has a non-zero opcode, it's likely a contract interaction (like Jetton)
	if intMsg.Body != nil {
		bodySlice := intMsg.Body.BeginParse()
		if bodySlice.BitsLeft() >= 32 {
			opcode, err := bodySlice.LoadUInt(32)
			if err == nil && opcode != 0 {
				// Non-zero opcode means this is NOT a simple transfer
				return nil, false
			}
		}
	}

	return &types.Transaction{
		TxHash:       base64.StdEncoding.EncodeToString(tx.Hash),
		NetworkId:    networkID,
		BlockNumber:  tx.LT,
		FromAddress:  intMsg.SrcAddr.String(),
		ToAddress:    dstAddr,
		AssetAddress: "", // Empty for native TON
		Amount:       amount,
		Type:         constant.TxTypeNativeTransfer, // Incoming transfer
		TxFee:        decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1e9)),
		Timestamp:    uint64(tx.Now),
		Status:       types.StatusConfirmed,
	}, true
}

// parseOutgoingTransfers handles outgoing TON transfers (send).
func parseOutgoingTransfers(tx *tlb.Transaction, ourAddress string, networkID string) []types.Transaction {
	var results []types.Transaction

	// Check for outgoing messages
	if tx.IO.Out == nil {
		return nil
	}

	// Iterate through outgoing messages to find transfers
	outList, err := tx.IO.Out.ToSlice()
	if err != nil {
		return nil
	}

	for _, outMsg := range outList {
		intMsg, ok := outMsg.Msg.(*tlb.InternalMessage)
		if !ok {
			continue
		}

		// Verify source is our account
		srcAddr := intMsg.SrcAddr.String()
		if srcAddr != ourAddress {
			continue
		}

		// Extract amount
		amount := intMsg.Amount.Nano().String()
		if amount == "0" {
			continue
		}

		// Skip if it has a non-zero opcode (likely contract interaction)
		if intMsg.Body != nil {
			bodySlice := intMsg.Body.BeginParse()
			if bodySlice.BitsLeft() >= 32 {
				opcode, err := bodySlice.LoadUInt(32)
				if err == nil && opcode != 0 {
					continue
				}
			}
		}

		results = append(results, types.Transaction{
			TxHash:       base64.StdEncoding.EncodeToString(tx.Hash),
			NetworkId:    networkID,
			BlockNumber:  tx.LT,
			FromAddress:  srcAddr,
			ToAddress:    intMsg.DstAddr.String(),
			AssetAddress: "",
			Amount:       amount,
			Type:         constant.TxTypeNativeTransfer,
			TxFee:        decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1e9)),
			Timestamp:    uint64(tx.Now),
			Status:       types.StatusConfirmed,
		})
	}

	return results
}
