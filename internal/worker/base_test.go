package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
)

const (
	testSourceAmountKey = "test_source_amount"
	testSourceAssetKey  = "test_source_asset"
	testSourceTypeKey   = "test_source_type"
)

type capturePubkeyStore struct {
	addresses map[string]struct{}
}

func (m capturePubkeyStore) Exist(_ enum.NetworkType, address string) bool {
	_, ok := m.addresses[address]
	return ok
}

func (m capturePubkeyStore) ExistBatch(_ enum.NetworkType, addresses []string) []bool {
	results := make([]bool, len(addresses))
	for i, addr := range addresses {
		_, results[i] = m.addresses[addr]
	}
	return results
}

func (m capturePubkeyStore) Save(enum.NetworkType, string) error { return nil }
func (m capturePubkeyStore) Close() error                        { return nil }

type captureEmitter struct {
	txs []types.Transaction
}

func (e *captureEmitter) EmitBlock(string, *types.Block) error    { return nil }
func (e *captureEmitter) EmitUTXO(string, *types.UTXOEvent) error { return nil }
func (e *captureEmitter) EmitError(string, error) error           { return nil }
func (e *captureEmitter) Emit(events.IndexerEvent) error          { return nil }
func (e *captureEmitter) Close()                                  {}
func (e *captureEmitter) EmitTransaction(_ string, tx *types.Transaction) error {
	if tx == nil {
		return errors.New("nil tx")
	}
	e.txs = append(e.txs, *tx)
	return nil
}

type directionalTestIndexer struct{}

var _ indexer.Indexer = (*directionalTestIndexer)(nil)
var _ indexer.DirectionalNormalizer = (*directionalTestIndexer)(nil)

func (directionalTestIndexer) GetName() string                  { return "DIRECTIONAL_TEST" }
func (directionalTestIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeStellar }
func (directionalTestIndexer) GetNetworkInternalCode() string   { return "directional_test" }
func (directionalTestIndexer) GetLatestBlockNumber(context.Context) (uint64, error) {
	return 0, nil
}
func (directionalTestIndexer) GetBlock(context.Context, uint64) (*types.Block, error) {
	return nil, nil
}
func (directionalTestIndexer) GetBlocks(context.Context, uint64, uint64, bool) ([]indexer.BlockResult, error) {
	return nil, nil
}
func (directionalTestIndexer) GetBlocksByNumbers(context.Context, []uint64) ([]indexer.BlockResult, error) {
	return nil, nil
}
func (directionalTestIndexer) IsHealthy() bool { return true }

func (directionalTestIndexer) NormalizeForDirection(tx types.Transaction, direction string) types.Transaction {
	if direction == types.DirectionOut {
		if sourceType := tx.GetMetadataString(testSourceTypeKey); sourceType != "" {
			tx.Type = constant.TxType(sourceType)
		}
		if sourceAmount := tx.GetMetadataString(testSourceAmountKey); sourceAmount != "" {
			tx.Amount = sourceAmount
		}
		switch tx.Type {
		case constant.TxTypeNativeTransfer:
			tx.AssetAddress = ""
		case constant.TxTypeTokenTransfer:
			tx.AssetAddress = tx.GetMetadataString(testSourceAssetKey)
		}
	}

	if tx.Metadata != nil {
		delete(tx.Metadata, testSourceTypeKey)
		delete(tx.Metadata, testSourceAmountKey)
		delete(tx.Metadata, testSourceAssetKey)
		if len(tx.Metadata) == 0 {
			tx.Metadata = nil
		}
	}
	return tx
}

func TestBaseWorkerEmitBlock_AppliesTokenOutgoingOverrides(t *testing.T) {
	t.Parallel()

	emitter := &captureEmitter{}
	bw := &BaseWorker{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: config.ChainConfig{TwoWayIndexing: true},
		chain:  directionalTestIndexer{},
		pubkeyStore: capturePubkeyStore{addresses: map[string]struct{}{
			"GSOURCE": {},
			"GDEST":   {},
		}},
		emitter: emitter,
	}

	block := &types.Block{
		Transactions: []types.Transaction{
			{
				TxHash:        "tx-path",
				NetworkId:     "stellar_mainnet",
				BlockHash:     "block-hash",
				TransferIndex: "payment-1",
				FromAddress:   "GSOURCE",
				ToAddress:     "GDEST",
				Amount:        "5.0000000",
				Type:          constant.TxTypeNativeTransfer,
				Memo:          "invoice-42",
				Metadata: map[string]any{
					testSourceTypeKey:     string(constant.TxTypeTokenTransfer),
					testSourceAmountKey:   "10.0000000",
					testSourceAssetKey:    "GISSUER:USDC",
				},
			},
		},
	}

	bw.emitBlock(block)
	if len(emitter.txs) != 2 {
		t.Fatalf("expected 2 emitted txs, got %d", len(emitter.txs))
	}

	inTx := emitter.txs[0]
	if inTx.Direction != types.DirectionIn {
		t.Fatalf("expected incoming direction, got %s", inTx.Direction)
	}
	if inTx.Type != constant.TxTypeNativeTransfer || inTx.Amount != "5.0000000" || inTx.AssetAddress != "" {
		t.Fatalf("incoming tx should keep receiver-side routing, got type=%s amount=%s asset=%s", inTx.Type, inTx.Amount, inTx.AssetAddress)
	}
	if inTx.Memo != "invoice-42" {
		t.Fatalf("incoming tx should preserve unrelated metadata")
	}
	if inTx.GetMetadataString(testSourceTypeKey) != "" || inTx.GetMetadataString(testSourceAmountKey) != "" || inTx.GetMetadataString(testSourceAssetKey) != "" {
		t.Fatalf("incoming tx should not leak directional override metadata")
	}

	outTx := emitter.txs[1]
	if outTx.Direction != types.DirectionOut {
		t.Fatalf("expected outgoing direction, got %s", outTx.Direction)
	}
	if outTx.Type != constant.TxTypeTokenTransfer || outTx.Amount != "10.0000000" || outTx.AssetAddress != "GISSUER:USDC" {
		t.Fatalf("outgoing tx should use source-side routing, got type=%s amount=%s asset=%s", outTx.Type, outTx.Amount, outTx.AssetAddress)
	}
	if outTx.Memo != "invoice-42" {
		t.Fatalf("outgoing tx should preserve unrelated metadata")
	}
	if outTx.GetMetadataString(testSourceTypeKey) != "" || outTx.GetMetadataString(testSourceAmountKey) != "" || outTx.GetMetadataString(testSourceAssetKey) != "" {
		t.Fatalf("outgoing tx should not leak directional override metadata")
	}
}

func TestBaseWorkerEmitBlock_AppliesNativeOutgoingOverrides(t *testing.T) {
	t.Parallel()

	emitter := &captureEmitter{}
	bw := &BaseWorker{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: config.ChainConfig{TwoWayIndexing: true},
		chain:  directionalTestIndexer{},
		pubkeyStore: capturePubkeyStore{addresses: map[string]struct{}{
			"GSOURCE": {},
			"GDEST":   {},
		}},
		emitter: emitter,
	}

	block := &types.Block{
		Transactions: []types.Transaction{
			{
				TxHash:        "tx-path",
				NetworkId:     "stellar_mainnet",
				BlockHash:     "block-hash",
				TransferIndex: "payment-2",
				FromAddress:   "GSOURCE",
				ToAddress:     "GDEST",
				Amount:        "5.0000000",
				Type:          constant.TxTypeTokenTransfer,
				AssetAddress:  "GISSUER:USDC",
				MemoType:      types.MemoTypeText,
				Metadata: map[string]any{
					testSourceTypeKey:         string(constant.TxTypeNativeTransfer),
					testSourceAmountKey:       "7.5000000",
				},
			},
		},
	}

	bw.emitBlock(block)
	if len(emitter.txs) != 2 {
		t.Fatalf("expected 2 emitted txs, got %d", len(emitter.txs))
	}

	outTx := emitter.txs[1]
	if outTx.Direction != types.DirectionOut {
		t.Fatalf("expected outgoing direction, got %s", outTx.Direction)
	}
	if outTx.Type != constant.TxTypeNativeTransfer || outTx.Amount != "7.5000000" || outTx.AssetAddress != "" {
		t.Fatalf("outgoing native override should clear receiver-side asset, got type=%s amount=%s asset=%s", outTx.Type, outTx.Amount, outTx.AssetAddress)
	}
	if outTx.MemoType != types.MemoTypeText {
		t.Fatalf("outgoing tx should preserve unrelated metadata")
	}
	if outTx.GetMetadataString(testSourceTypeKey) != "" || outTx.GetMetadataString(testSourceAmountKey) != "" || outTx.GetMetadataString(testSourceAssetKey) != "" {
		t.Fatalf("outgoing tx should not leak directional override metadata")
	}
}
