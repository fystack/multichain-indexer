package ton

import (
	"encoding/base64"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
	"github.com/xssnick/tonutils-go/tlb"
)

// Jetton standard opcodes (TEP-74)
const (
	OpcodeTransfer             uint64 = 0x0f8a7ea5
	OpcodeTransferNotification uint64 = 0x7362d09c
)

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

// ParseJettonTransfer extracts a Jetton transfer from a transaction.
// Detects both incoming (receive) and outgoing (send) Jetton transfers.
// Returns a slice of parsed transactions involving our address.
func ParseJettonTransfer(tx *tlb.Transaction, ourAddress string, networkID string, registry JettonRegistry) []types.Transaction {
	var results []types.Transaction

	// Try incoming transfer notification first (receive)
	if parsed, ok := parseIncomingJetton(tx, ourAddress, networkID, registry); ok {
		results = append(results, *parsed)
	}

	// Try outgoing transfers (send) - can be multiple
	results = append(results, parseOutgoingJettons(tx, ourAddress, networkID, registry)...)

	return results
}

// parseIncomingJetton handles incoming Jetton transfer notifications (receive).
func parseIncomingJetton(tx *tlb.Transaction, ourAddress string, networkID string, registry JettonRegistry) (*types.Transaction, bool) {
	// Check for incoming internal message
	if tx.IO.In == nil {
		return nil, false
	}

	// Only process internal messages
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

	// Parse message body for transfer_notification
	if intMsg.Body == nil {
		return nil, false
	}

	bodySlice := intMsg.Body.BeginParse()
	opcode, err := bodySlice.LoadUInt(32)
	if err != nil || opcode != OpcodeTransferNotification {
		return nil, false
	}

	// Structure: query_id:uint64 amount:VarUInteger16 sender:Address forward_payload:Either Cell ^Cell
	_, err = bodySlice.LoadUInt(64) // query_id
	if err != nil {
		return nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return nil, false
	}

	sender, err := bodySlice.LoadAddr()
	if err != nil {
		return nil, false
	}

	// The sender of the notification is the Jetton wallet
	jettonWallet := intMsg.SrcAddr.String()

	// Try to get Jetton info from registry
	var assetAddress string
	if registry != nil {
		if info, ok := registry.GetInfoByWallet(jettonWallet); ok {
			assetAddress = info.MasterAddress
		} else {
			// If not found in cache, it might be an unknown Jetton
			// or we need to wait for a registration
			assetAddress = jettonWallet
		}
	} else {
		assetAddress = jettonWallet
	}

	return &types.Transaction{
		TxHash:       base64.StdEncoding.EncodeToString(tx.Hash),
		NetworkId:    networkID,
		BlockNumber:  tx.LT,
		FromAddress:  sender.String(),
		ToAddress:    ourAddress,
		AssetAddress: assetAddress,
		Amount:       jettonAmount.String(),
		Type:         constant.TxTypeTokenTransfer,
		TxFee:        decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1e9)),
		Timestamp:    uint64(tx.Now),
		Status:       types.StatusConfirmed,
	}, true
}

// parseOutgoingJettons handles outgoing Jetton transfers (send).
func parseOutgoingJettons(tx *tlb.Transaction, ourAddress string, networkID string, registry JettonRegistry) []types.Transaction {
	var results []types.Transaction

	// Check for outgoing messages
	if tx.IO.Out == nil {
		return nil
	}

	outList, err := tx.IO.Out.ToSlice()
	if err != nil {
		return nil
	}

	for _, outMsg := range outList {
		intMsg, ok := outMsg.Msg.(*tlb.InternalMessage)
		if !ok {
			continue
		}

		// Parse message body for transfer opcode
		if intMsg.Body == nil {
			continue
		}

		bodySlice := intMsg.Body.BeginParse()
		opcode, err := bodySlice.LoadUInt(32)
		if err != nil || opcode != OpcodeTransfer {
			continue
		}

		// Parse transfer fields
		// Skip query_id
		_, err = bodySlice.LoadUInt(64)
		if err != nil {
			continue
		}

		// Load Jetton amount
		jettonAmount, err := bodySlice.LoadVarUInt(16)
		if err != nil {
			continue
		}

		// Load destination address
		destination, err := bodySlice.LoadAddr()
		if err != nil {
			continue
		}

		// Get Jetton wallet (the destination of the outgoing message is our Jetton wallet)
		jettonWallet := intMsg.DstAddr.String()

		// Try to get Jetton info from registry
		var assetAddress string
		if registry != nil {
			if info, ok := registry.GetInfoByWallet(jettonWallet); ok {
				assetAddress = info.MasterAddress
			} else {
				assetAddress = jettonWallet
			}
		} else {
			assetAddress = jettonWallet
		}

		results = append(results, types.Transaction{
			TxHash:       base64.StdEncoding.EncodeToString(tx.Hash),
			NetworkId:    networkID,
			BlockNumber:  tx.LT,
			FromAddress:  ourAddress,
			ToAddress:    destination.String(),
			AssetAddress: assetAddress,
			Amount:       jettonAmount.String(),
			Type:         constant.TxTypeTokenTransfer,
			TxFee:        decimal.NewFromBigInt(tx.TotalFees.Coins.Nano(), 0).Div(decimal.NewFromInt(1e9)),
			Timestamp:    uint64(tx.Now),
			Status:       types.StatusConfirmed,
		})
	}

	return results
}
