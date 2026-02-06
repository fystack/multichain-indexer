package ton

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/stretchr/testify/assert"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// MockRegistry implements JettonRegistry for testing
type MockRegistry struct {
	info map[string]JettonInfo
}

func (m *MockRegistry) IsSupported(walletAddress string) bool {
	return false
}

func (m *MockRegistry) GetInfo(masterAddress string) (*JettonInfo, bool) {
	return nil, false
}

func (m *MockRegistry) GetInfoByWallet(walletAddress string) (*JettonInfo, bool) {
	if info, ok := m.info[walletAddress]; ok {
		return &info, true
	}
	return nil, false
}

func (m *MockRegistry) RegisterWallet(walletAddress, masterAddress string) {}
func (m *MockRegistry) List() []JettonInfo                                 { return nil }

func TestParseJettonTransfer(t *testing.T) {
	ourAddr := address.MustParseAddr("Ef8zMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM0vF")    // Burn address
	senderAddr := address.MustParseAddr("EQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAM9c") // Zero address
	// The jetton wallet that sends us the notification
	jettonWalletAddr := address.MustParseAddr("EQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAM9c") // Zero address

	t.Run("valid transfer notification", func(t *testing.T) {
		// Mock registry
		registry := &MockRegistry{
			info: map[string]JettonInfo{
				jettonWalletAddr.String(): {
					MasterAddress: "EQMaster...",
					Symbol:        "USDT",
					Decimals:      6,
				},
			},
		}

		// Construct transfer notification body
		// Opcode: 0x7362d09c (transfer_notification)
		// query_id: uint64
		// amount: Coins
		// sender: MsgAddress
		// forward_payload: Either Cell ^Cell
		body := cell.BeginCell().
			MustStoreUInt(0x7362d09c, 32). // Opcode
			MustStoreUInt(0, 64).          // QueryID
			MustStoreCoins(1000000).       // Amount (1 USDT)
			MustStoreAddr(senderAddr).     // Original sender
			MustStoreRef(                  // Forward payload (comment)
				cell.BeginCell().
					MustStoreUInt(0, 32). // Text comment opcode
					MustStoreStringSnake("hello jetton").
					EndCell(),
			).
			EndCell()

		tx := &tlb.Transaction{
			LT:  2000,
			Now: uint32(time.Now().Unix()),
		}

		tx.IO.In = &tlb.Message{
			MsgType: tlb.MsgTypeInternal,
			Msg: &tlb.InternalMessage{
				SrcAddr: jettonWalletAddr, // Notification comes from jetton wallet
				DstAddr: ourAddr,
				Amount:  tlb.MustFromTON("0.1"), // Small amount of TON attached
				Body:    body,
			},
		}

		tx.Hash = []byte("txhash")

		results := ParseJettonTransfer(tx, ourAddr.String(), "TON_MAINNET", registry)
		assert.Len(t, results, 1)
		parsed := results[0]
		assert.Equal(t, "1000000", parsed.Amount)
		assert.Equal(t, senderAddr.String(), parsed.FromAddress)
		assert.Equal(t, ourAddr.String(), parsed.ToAddress)
		assert.Equal(t, "EQMaster...", parsed.AssetAddress)
		assert.Equal(t, base64.StdEncoding.EncodeToString(tx.Hash), parsed.TxHash)
		assert.Equal(t, constant.TxTypeTokenTransfer, parsed.Type)
	})
}
