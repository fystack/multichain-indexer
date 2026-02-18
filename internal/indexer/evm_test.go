package indexer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ethMainnetRPC = "https://ethereum-rpc.publicnode.com"

func newTestEVMClient() *evm.Client {
	return evm.NewEthereumClient(ethMainnetRPC, nil, 30*time.Second, nil)
}

func newTestEVMIndexer() *EVMIndexer {
	return &EVMIndexer{
		chainName:   "ethereum",
		config:      config.ChainConfig{NetworkId: "ethereum-mainnet"},
		pubkeyStore: nil, // no filtering
	}
}

// TestParseGnosisSafeETHTransfer tests indexing a Gnosis Safe execTransaction that transfers 0.1 ETH internally.
// Transaction: 0x7c98ff7c910b025736b11d2f70db001d5c2ec25df6de9fb65193963f6059b1f9
// Block: 22869070
// From: 0xA768d264b8bF98588EBdEF6E241a0a73bAF287D1
// To (Safe contract): apescreener-treasury.eth
// Internal transfer: 0.1 ETH from Safe to 0xc26dC13d...d1C5e3e9d
//
// This is a Gnosis Safe execTransaction call where the actual ETH transfer happens
// as an internal transaction (trace), not as a direct value transfer on the outer tx.
func TestParseGnosisSafeETHTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTestEVMClient()

	txHash := "0x7c98ff7c910b025736b11d2f70db001d5c2ec25df6de9fb65193963f6059b1f9"
	blockNumber := uint64(22869070)
	blockHex := fmt.Sprintf("0x%x", blockNumber)

	// Fetch the block containing the transaction
	block, err := c.GetBlockByNumber(ctx, blockHex, true)
	require.NoError(t, err)
	require.NotNil(t, block, "block should exist on mainnet")

	// Find the specific transaction in the block
	var targetTx *evm.Txn
	for i := range block.Transactions {
		if block.Transactions[i].Hash == txHash {
			targetTx = &block.Transactions[i]
			break
		}
	}
	require.NotNil(t, targetTx, "transaction should exist in block %d", blockNumber)

	t.Logf("Transaction found: hash=%s from=%s to=%s value=%s input_len=%d",
		targetTx.Hash, targetTx.From, targetTx.To, targetTx.Value, len(targetTx.Input))

	// Fetch the receipt
	receipts, err := c.BatchGetTransactionReceipts(ctx, []string{txHash})
	require.NoError(t, err)
	receipt := receipts[txHash]
	require.NotNil(t, receipt, "receipt should exist")

	t.Logf("Receipt: status=%s gasUsed=%s logs=%d", receipt.Status, receipt.GasUsed, len(receipt.Logs))

	// Build a mini block with just this transaction and process through the indexer
	miniBlock := &evm.Block{
		Number:       block.Number,
		Hash:         block.Hash,
		ParentHash:   block.ParentHash,
		Timestamp:    block.Timestamp,
		Transactions: []evm.Txn{*targetTx},
	}

	idx := newTestEVMIndexer()
	receiptMap := map[string]*evm.TxnReceipt{txHash: receipt}
	typesBlock, err := idx.convertBlock(miniBlock, receiptMap)
	require.NoError(t, err)

	t.Logf("Extracted %d transfers from block", len(typesBlock.Transactions))
	for i, tx := range typesBlock.Transactions {
		t.Logf("  Transfer[%d]: type=%s from=%s to=%s amount=%s asset=%s",
			i, tx.Type, tx.FromAddress, tx.ToAddress, tx.Amount, tx.AssetAddress)
	}

	// This Gnosis Safe execTransaction has value=0x0 on the outer tx and the actual
	// 0.1 ETH (100000000000000000 wei) transfer happens as an internal transaction.
	// Current indexer behavior: the outer tx has value=0 and a non-empty input (execTransaction),
	// so it does NOT match native transfer detection (which requires value > 0 and empty input).
	// The receipt logs don't contain ERC20 Transfer events either (it's a native ETH internal tx).
	//
	// This test documents that Gnosis Safe internal ETH transfers are NOT currently indexed.
	// To support these, the indexer would need to use debug_traceTransaction or trace_transaction
	// to capture internal calls.

	var nativeTransfer, tokenTransfer bool
	for _, tx := range typesBlock.Transactions {
		if tx.Type == constant.TxTypeNativeTransfer {
			nativeTransfer = true
		}
		if tx.Type == constant.TxTypeTokenTransfer {
			tokenTransfer = true
		}
	}

	// Document current behavior: Gnosis Safe internal ETH transfers are not detected
	assert.False(t, nativeTransfer, "Gnosis Safe internal ETH transfer is NOT detected as native transfer (value=0 on outer tx)")
	assert.False(t, tokenTransfer, "No ERC20 token transfer expected for this ETH-only Gnosis Safe tx")
	assert.Empty(t, typesBlock.Transactions, "No transfers extracted - Gnosis Safe internal txs require trace support")

	t.Log("RESULT: Gnosis Safe execTransaction with internal ETH transfer is NOT indexed.")
	t.Log("The indexer would need trace/debug RPC support to capture internal ETH transfers.")
}
