package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/bitcoin"
)

// TestBitcoinPrevoutVerbosity confirms that getblock verbosity=3 includes prevout
// data, eliminating the need for per-transaction prevout resolution.
func TestBitcoinPrevoutVerbosity(t *testing.T) {
	const rpcURL = "https://bitcoin-testnet-rpc.publicnode.com"
	const targetBlock = uint64(4842314)

	client := bitcoin.NewBitcoinClient(rpcURL, nil, 30*time.Second, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Verbosity 2 (old) - no prevout
	t1 := time.Now()
	block2, err := client.GetBlockByHeight(ctx, targetBlock, 2)
	if err != nil {
		t.Fatalf("verbosity=2 failed: %v", err)
	}
	dur2 := time.Since(t1)

	// Verbosity 3 (new) - with prevout
	t2 := time.Now()
	block3, err := client.GetBlockByHeight(ctx, targetBlock, 3)
	if err != nil {
		t.Fatalf("verbosity=3 failed: %v", err)
	}
	dur3 := time.Since(t2)

	// Count prevouts in each
	prevout2, noPrevout2 := countPrevouts(block2.Tx)
	prevout3, noPrevout3 := countPrevouts(block3.Tx)

	t.Logf("verbosity=2: %d with prevout, %d without (took %v)", prevout2, noPrevout2, dur2)
	t.Logf("verbosity=3: %d with prevout, %d without (took %v)", prevout3, noPrevout3, dur3)

	if noPrevout3 > 0 {
		t.Errorf("verbosity=3 should have all prevouts filled, but %d are missing", noPrevout3)
	}
	if prevout3 == 0 {
		t.Error("verbosity=3 returned no prevout data")
	}
	t.Logf("verbosity=3 eliminates all %d prevout resolution calls", noPrevout2)
}

func countPrevouts(txs []bitcoin.Transaction) (withPrevout, withoutPrevout int) {
	for _, tx := range txs {
		if tx.IsCoinbase() {
			continue
		}
		if len(tx.Vin) > 0 && tx.Vin[0].PrevOut != nil {
			withPrevout++
		} else {
			withoutPrevout++
		}
	}
	return
}
