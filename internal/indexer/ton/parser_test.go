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

func TestParseTonTransfer(t *testing.T) {
	// Burn address (valid)
	ourAddr := address.MustParseAddr("Ef8zMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM0vF")
	// Zero address (valid)
	senderAddr := address.MustParseAddr("EQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAM9c")

	t.Run("valid receive", func(t *testing.T) {
		// Construct a transaction
		tx := &tlb.Transaction{
			LT:  1000,
			Now: uint32(time.Now().Unix()),
		}

		// Setup incoming internal message safely
		tx.IO.In = &tlb.Message{
			MsgType: tlb.MsgTypeInternal,
			Msg: &tlb.InternalMessage{
				SrcAddr: senderAddr,
				DstAddr: ourAddr,
				Amount:  tlb.MustFromTON("1"), // 1 TON
				Body:    cell.BeginCell().EndCell(),
			},
		}

		tx.Hash = []byte("txhash")

		results := ParseTonTransfer(tx, ourAddr.String(), "TON_MAINNET")
		assert.Len(t, results, 1)
		parsed := results[0]
		assert.Equal(t, "1000000000", parsed.Amount)
		assert.Equal(t, senderAddr.String(), parsed.FromAddress)
		assert.Equal(t, ourAddr.String(), parsed.ToAddress)
		assert.Equal(t, base64.StdEncoding.EncodeToString(tx.Hash), parsed.TxHash)
		assert.Equal(t, constant.TxTypeNativeTransfer, parsed.Type)
	})

	t.Run("empty transaction", func(t *testing.T) {
		tx := &tlb.Transaction{}
		results := ParseTonTransfer(tx, ourAddr.String(), "TON_MAINNET")
		assert.Len(t, results, 0)
	})

	t.Run("ignore other destination", func(t *testing.T) {
		otherAddr := address.MustParseAddr("EQD__________________________________________0vo")
		tx := &tlb.Transaction{
			LT: 1000,
		}
		tx.IO.In = &tlb.Message{
			MsgType: tlb.MsgTypeInternal,
			Msg: &tlb.InternalMessage{
				SrcAddr: senderAddr,
				DstAddr: otherAddr,
				Amount:  tlb.MustFromTON("1"),
			},
		}

		results := ParseTonTransfer(tx, ourAddr.String(), "TON_MAINNET")
		assert.Len(t, results, 0)
	})

	t.Run("parse comment", func(t *testing.T) {
		comment := "hello world"
		body := cell.BeginCell().
			MustStoreUInt(0, 32). // Opcode 0 for text comment
			MustStoreStringSnake(comment).
			EndCell()

		tx := &tlb.Transaction{
			LT: 1000,
		}
		tx.IO.In = &tlb.Message{
			MsgType: tlb.MsgTypeInternal,
			Msg: &tlb.InternalMessage{
				SrcAddr: senderAddr,
				DstAddr: ourAddr,
				Amount:  tlb.MustFromTON("0.00000001"),
				Body:    body,
			},
		}

		results := ParseTonTransfer(tx, ourAddr.String(), "TON_MAINNET")
		assert.Len(t, results, 1)
	})
}
