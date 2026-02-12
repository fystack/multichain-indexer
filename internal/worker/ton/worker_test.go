package ton

import (
	"io"
	"log/slog"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
)

func TestShouldSkipTrackedInternalTransfer(t *testing.T) {
	const (
		sender   = "0:2942e40f94b5a2f111ea2ff98beb5f634f3a971f99f7fedafff5164c4bfa1bef"
		receiver = "0:fc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e"
		external = "0:5df8318107d8988e2ab298b0190ba9f923267bc1575f4532a49f37384db6a799"
	)

	w := &TonPollingWorker{}
	w.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	w.replaceWalletCache([]string{sender, receiver})

	tx := &types.Transaction{
		FromAddress: sender,
		ToAddress:   receiver,
	}

	t.Run("keep_sender_side_poll", func(t *testing.T) {
		assert.False(t, w.shouldSkipTrackedInternalTransfer(sender, tx))
	})

	t.Run("skip_receiver_side_poll", func(t *testing.T) {
		assert.True(t, w.shouldSkipTrackedInternalTransfer(receiver, tx))
	})

	t.Run("do_not_skip_external_to_tracked", func(t *testing.T) {
		externalTx := &types.Transaction{
			FromAddress: external,
			ToAddress:   receiver,
		}
		assert.False(t, w.shouldSkipTrackedInternalTransfer(receiver, externalTx))
	})
}
