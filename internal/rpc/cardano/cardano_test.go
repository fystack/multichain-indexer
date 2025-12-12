package cardano

import (
	"context"
	"os"
	"testing"
	"time"

	rpclib "github.com/fystack/multichain-indexer/internal/rpc"
)

// newClient creates a Cardano client using Blockfrost and the env API key.
func newClient(t *testing.T) *CardanoClient {
	t.Helper()
	apiKey := os.Getenv("BLOCKFROST_API_KEY")
	if apiKey == "" {
		t.Skip("skipping: BLOCKFROST_API_KEY not set (export your Blockfrost project_id)")
	}
	return NewCardanoClient(
		"https://cardano-mainnet.blockfrost.io/api/v0",
		&rpclib.AuthConfig{Type: rpclib.AuthTypeHeader, Key: "project_id", Value: apiKey},
		10*time.Second,
		nil,
	)
}

func TestCardanoGetLatestBlockNumber(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newClient(t)
	ctx := context.Background()
	bn, err := client.GetLatestBlockNumber(ctx)
	if err != nil {
		t.Fatalf("GetLatestBlockNumber failed: %v", err)
	}
	if bn == 0 {
		t.Fatal("expected non-zero latest block number")
	}
	t.Logf("latest: %d", bn)
}

func TestCardanoGetBlockByNumber(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newClient(t)
	ctx := context.Background()

	latest, err := client.GetLatestBlockNumber(ctx)
	if err != nil {
		t.Fatalf("GetLatestBlockNumber failed: %v", err)
	}
	// pick a recent block to avoid head reorgs
	target := latest
	if latest > 5 {
		target = latest - 5
	}
	blk, err := client.GetBlockByNumber(ctx, target)
	if err != nil {
		t.Fatalf("GetBlockByNumber(%d) failed: %v", target, err)
	}
	if blk == nil || blk.Hash == "" {
		t.Fatalf("invalid block returned: %+v", blk)
	}
	t.Logf("block %d hash=%s txs=%d", blk.Height, blk.Hash, len(blk.Txs))
}

func TestCardanoGetBlockByHash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newClient(t)
	ctx := context.Background()

	latest, err := client.GetLatestBlockNumber(ctx)
	if err != nil {
		t.Fatalf("GetLatestBlockNumber failed: %v", err)
	}
	hash, err := client.GetBlockHash(ctx, latest)
	if err != nil {
		t.Fatalf("GetBlockHash failed: %v", err)
	}
	blk, err := client.GetBlockByHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlockByHash failed: %v", err)
	}
	if blk == nil || blk.Hash == "" {
		t.Fatalf("invalid block returned: %+v", blk)
	}
	t.Logf("block by hash %s -> height=%d txs=%d", hash, blk.Height, len(blk.Txs))
}

func TestCardanoFetchTransactionsParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := newClient(t)
	ctx := context.Background()

	latest, err := client.GetLatestBlockNumber(ctx)
	if err != nil {
		t.Fatalf("GetLatestBlockNumber failed: %v", err)
	}
	hashes, err := client.GetTransactionsByBlock(ctx, latest)
	if err != nil {
		t.Fatalf("GetTransactionsByBlock failed: %v", err)
	}
	if len(hashes) == 0 {
		t.Skip("no txs in latest block")
	}
	if len(hashes) > 5 {
		hashes = hashes[:5] // limit to avoid quota
	}
	txs, err := client.FetchTransactionsParallel(ctx, hashes, 3)
	if err != nil {
		t.Fatalf("FetchTransactionsParallel failed: %v", err)
	}
	if len(txs) == 0 {
		t.Fatal("expected some transactions")
	}
}

