package indexer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type evmPubkeyStoreStub struct {
	addresses map[string]bool
}

func (s evmPubkeyStoreStub) Exist(_ enum.NetworkType, address string) bool {
	return s.addresses[address]
}

func (s evmPubkeyStoreStub) ExistBatch(_ enum.NetworkType, addresses []string) []bool {
	results := make([]bool, len(addresses))
	for i, addr := range addresses {
		results[i] = s.addresses[addr]
	}
	return results
}

const safeExecInputTest = "0x6a761202000000000000000000000000c26dc13d057824342d5480b153f288bd1c5e3e9d000000000000000000000000000000000000000000000000016345785d8a000000000000000000000000000000000000000000000000000000000000000001400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001600000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000008200000000000000000000000040a1ade6e21129c09cfb3c501efba5e56ba9a3e50000000000000000000000000000000000000000000000000000000000000000010000000000000000000000005c45ee16de1570b662e32e7960e1e501dd93cde4000000000000000000000000000000000000000000000000000000000000000001a5aa81e56a92b6f46bf78edb6a34e0086c0bb5c1e9d2c20ae60d49c1a8de6b4f15c14847f1ba3f6c35b51dbefae4c74a01bf7c28a0a0c53fb4e73a6d40e36f76b1c00000000000000000000000000000000000000000000000000000000000000"

const ethMainnetRPC = "https://ethereum-rpc.publicnode.com"
const sepoliaRPC = "https://ethereum-sepolia-rpc.publicnode.com"

func newTestEVMClient() *evm.Client {
	return evm.NewEthereumClient(ethMainnetRPC, nil, 30*time.Second, nil)
}

func newTestSepoliaClient() *evm.Client {
	return evm.NewEthereumClient(sepoliaRPC, nil, 30*time.Second, nil)
}

func newTestEVMIndexer() *EVMIndexer {
	return &EVMIndexer{
		chainName:   "ethereum",
		config:      config.ChainConfig{NetworkId: "ethereum-mainnet"},
		pubkeyStore: nil, // no filtering
	}
}

func newTestSepoliaIndexer() *EVMIndexer {
	return &EVMIndexer{
		chainName:   "sepolia",
		config:      config.ChainConfig{NetworkId: "ethereum-sepolia"},
		pubkeyStore: nil,
	}
}

func TestExtractReceiptTxHashes_SelectsSafeOutgoingWhenTwoWayEnabled(t *testing.T) {
	idx := &EVMIndexer{
		chainName: "ethereum",
		config: config.ChainConfig{
			NetworkId:      "ethereum-mainnet",
			TwoWayIndexing: true,
		},
		pubkeyStore: evmPubkeyStoreStub{
			addresses: map[string]bool{
				evm.ToChecksumAddress("0x84ba2321d46814fb1aa69a7b71882efea50f700c"): true,
			},
		},
	}

	blockNum := uint64(123)
	blocks := map[uint64]*evm.Block{
		blockNum: {
			Transactions: []evm.Txn{
				{
					Hash:  "0xabc123",
					From:  "0xa768d264b8bf98588ebdef6e241a0a73baf287d1",
					To:    "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
					Input: safeExecInputTest,
				},
			},
		},
	}

	got := idx.extractReceiptTxHashes(blocks, false)
	require.Equal(t, []string{"0xabc123"}, got[blockNum])
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
	typesBlock, err := idx.convertBlock(miniBlock, receiptMap, nil, nil)
	require.NoError(t, err)

	t.Logf("Extracted %d transfers from block", len(typesBlock.Transactions))
	for i, tx := range typesBlock.Transactions {
		t.Logf("  Transfer[%d]: type=%s from=%s to=%s amount=%s asset=%s",
			i, tx.Type, tx.FromAddress, tx.ToAddress, tx.Amount, tx.AssetAddress)
	}

	// The indexer decodes execTransaction input data and verifies the ExecutionSuccess
	// event in the receipt to extract the internal native transfer.

	var nativeTransfer *types.Transaction
	for i, tx := range typesBlock.Transactions {
		if tx.Type == constant.TxTypeNativeTransfer {
			nativeTransfer = &typesBlock.Transactions[i]
		}
	}

	require.NotNil(t, nativeTransfer, "Gnosis Safe internal ETH transfer SHOULD be detected as native_transfer")
	assert.Equal(t, evm.ToChecksumAddress("0x84ba2321d46814fb1aa69a7b71882efea50f700c"), nativeTransfer.FromAddress, "from should be the Safe contract")
	assert.Equal(t, evm.ToChecksumAddress("0xc26dC13d057824342D5480b153f288bd1C5e3e9d"), nativeTransfer.ToAddress, "to should be the decoded recipient")
	assert.Equal(t, "100000000000000000", nativeTransfer.Amount, "amount should be 0.1 ETH in wei")

	t.Log("RESULT: Gnosis Safe execTransaction with internal ETH transfer IS indexed via input decoding.")
}

// TestParseGnosisSafeETHTransfer_Sepolia tests the same Gnosis Safe indexing on Sepolia testnet
// with a real transaction created for this test.
// Transaction: 0x7694b41ca7105e4080d1d172d3ad99293902c36bf83bb46d2d9bd6a316ba050b
// Block: 10356752
// From (EOA): 0x23dc93f83d34f66a96de2623915ce69852f34a13
// To (Safe contract): 0x13178e59d4b3ca1a06a6dcfa6692e1f6fbdb58c8
// Internal transfer: 0.1 ETH from Safe back to the EOA
func TestParseGnosisSafeETHTransfer_Sepolia(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	c := newTestSepoliaClient()

	txHash := "0x7694b41ca7105e4080d1d172d3ad99293902c36bf83bb46d2d9bd6a316ba050b"
	blockNumber := uint64(10356752)
	blockHex := fmt.Sprintf("0x%x", blockNumber)

	// Fetch the block containing the transaction
	block, err := c.GetBlockByNumber(ctx, blockHex, true)
	require.NoError(t, err)
	require.NotNil(t, block, "block should exist on Sepolia")

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

	// Verify this is a Safe execTransaction
	require.True(t, evm.IsSafeExecTransaction(targetTx.Input), "should be detected as Safe execTransaction")

	// Fetch the receipt
	receipts, err := c.BatchGetTransactionReceipts(ctx, []string{txHash})
	require.NoError(t, err)
	receipt := receipts[txHash]
	require.NotNil(t, receipt, "receipt should exist")

	t.Logf("Receipt: status=%s gasUsed=%s logs=%d", receipt.Status, receipt.GasUsed, len(receipt.Logs))
	for i, log := range receipt.Logs {
		topic0 := ""
		if len(log.Topics) > 0 {
			topic0 = log.Topics[0]
		}
		t.Logf("  Log[%d]: address=%s topic0=%s", i, log.Address, topic0)
	}

	// Build a mini block with just this transaction and process through the indexer
	miniBlock := &evm.Block{
		Number:       block.Number,
		Hash:         block.Hash,
		ParentHash:   block.ParentHash,
		Timestamp:    block.Timestamp,
		Transactions: []evm.Txn{*targetTx},
	}

	idx := newTestSepoliaIndexer()
	receiptMap := map[string]*evm.TxnReceipt{txHash: receipt}
	typesBlock, err := idx.convertBlock(miniBlock, receiptMap, nil, nil)
	require.NoError(t, err)

	t.Logf("Extracted %d transfers from block", len(typesBlock.Transactions))
	for i, tx := range typesBlock.Transactions {
		t.Logf("  Transfer[%d]: type=%s from=%s to=%s amount=%s",
			i, tx.Type, tx.FromAddress, tx.ToAddress, tx.Amount)
	}

	// Verify the Safe internal ETH transfer was detected
	safeAddr := "0x13178e59d4b3ca1a06a6dcfa6692e1f6fbdb58c8"
	recipientAddr := "0x23dc93f83d34f66a96de2623915ce69852f34a13"

	var nativeTransfer *types.Transaction
	for i, tx := range typesBlock.Transactions {
		if tx.Type == constant.TxTypeNativeTransfer {
			nativeTransfer = &typesBlock.Transactions[i]
		}
	}

	require.NotNil(t, nativeTransfer, "Gnosis Safe internal ETH transfer SHOULD be detected")
	assert.Equal(t, evm.ToChecksumAddress(safeAddr), nativeTransfer.FromAddress, "from should be the Safe contract")
	assert.Equal(t, evm.ToChecksumAddress(recipientAddr), nativeTransfer.ToAddress, "to should be the decoded recipient")
	assert.Equal(t, "100000000000000000", nativeTransfer.Amount, "amount should be 0.1 ETH in wei")

	t.Log("RESULT: Sepolia Gnosis Safe execTransaction with internal ETH transfer IS indexed correctly.")
}

// --- Trace feature verification tests ---

func TestConvertBlock_DebugTraceDisabled_SafeHeuristicUnchanged(t *testing.T) {
	// Regression: with debug_trace=false (traces=nil), Safe heuristic still works
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
	}

	receipt := &evm.TxnReceipt{
		TransactionHash: "0xtx1",
		Status:          "0x1",
		GasUsed:         "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
		Logs: []evm.Log{
			{
				Address: "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
				Topics:  []string{evm.SAFE_EXECUTION_SUCCESS_TOPIC, "0x0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}

	block := &evm.Block{
		Number:    "0x1",
		Timestamp: "0x60000000",
		Transactions: []evm.Txn{
			{
				Hash:  "0xtx1",
				From:  "0xsender",
				To:    "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
				Value: "0x0",
				Input: safeExecInputTest,
			},
		},
	}

	receiptMap := map[string]*evm.TxnReceipt{"0xtx1": receipt}
	canonicalSafes := map[string]bool{"0x84ba2321d46814fb1aa69a7b71882efea50f700c": true}
	result, err := idx.convertBlock(block, receiptMap, nil, canonicalSafes) // traces=nil
	require.NoError(t, err)

	// Safe heuristic should still extract the transfer
	var found bool
	for _, tx := range result.Transactions {
		if tx.Type == constant.TxTypeNativeTransfer {
			found = true
			assert.Equal(t, "100000000000000000", tx.Amount)
		}
	}
	assert.True(t, found, "Safe heuristic should still work when traces=nil")

	// The same calldata and ExecutionSuccess log from a contract not confirmed as an official
	// Safe is the fake-Safe attack: nothing may be credited.
	result, err = idx.convertBlock(block, receiptMap, nil, nil)
	require.NoError(t, err)
	for _, tx := range result.Transactions {
		assert.NotEqual(t, constant.TxTypeNativeTransfer, tx.Type, "unconfirmed Safe must not produce a native transfer")
	}
}

// safeProxyV130RuntimeTest is the official Safe v1.3.0 proxy runtime code.
const safeProxyV130RuntimeTest = "0x608060405273ffffffffffffffffffffffffffffffffffffffff600054167fa619486e0000000000000000000000000000000000000000000000000000000060003514156050578060005260206000f35b3660008037600080366000845af43d6000803e60008114156070573d6000fd5b3d6000f3fea2646970667358221220d1429297349653a4918076d650332de1a1068c5f3e07c5c82360c277770b955264736f6c63430007060033"

// safeV130SingletonSlotTest is slot 0 of a proxy pointing at the official v1.3.0 GnosisSafe.
const safeV130SingletonSlotTest = "0x000000000000000000000000d9db270c1b5e3bd161e8c8503c55ceabee709552"

type safeCheckClient struct {
	evm.EthereumAPI
	code  map[string]string
	slot0 map[string]string
	err   error
	calls int
}

func (c *safeCheckClient) GetCode(_ context.Context, address string) (string, error) {
	c.calls++
	return c.code[address], c.err
}

func (c *safeCheckClient) GetStorageAt(_ context.Context, address, _ string) (string, error) {
	return c.slot0[address], c.err
}

func TestCheckSafeCandidates(t *testing.T) {
	const (
		realSafe = "0x84ba2321d46814fb1aa69a7b71882efea50f700c"
		fakeSafe = "0x00000000000000000000000000000000000000fa"
	)
	safeTx := func(hash, to string) evm.Txn {
		return evm.Txn{Hash: hash, From: "0xsender", To: to, Value: "0x0", Input: safeExecInputTest}
	}
	successReceipt := func(hash, emitter string) *evm.TxnReceipt {
		return &evm.TxnReceipt{
			TransactionHash: hash,
			Status:          "0x1",
			Logs:            []evm.Log{{Address: emitter, Topics: []string{evm.SAFE_EXECUTION_SUCCESS_TOPIC}}},
		}
	}
	blocks := map[uint64]*evm.Block{
		1: {Number: "0x1", Transactions: []evm.Txn{safeTx("0xreal", realSafe)}},
		2: {Number: "0x2", Transactions: []evm.Txn{safeTx("0xfake", fakeSafe)}},
	}
	receipts := map[string]*evm.TxnReceipt{
		"0xreal": successReceipt("0xreal", realSafe),
		"0xfake": successReceipt("0xfake", fakeSafe),
	}
	newIndexer := func(client *safeCheckClient) *EVMIndexer {
		failover := rpc.NewFailover[evm.EthereumAPI](nil)
		require.NoError(t, failover.AddProvider(&rpc.Provider{Name: "test", URL: "http://evm.test", Network: "eth", Client: client, State: rpc.StateHealthy}))
		return &EVMIndexer{chainName: "ethereum", config: config.ChainConfig{NetworkId: "eth"}, failover: failover}
	}

	t.Run("only an official Safe proxy with an official singleton is confirmed", func(t *testing.T) {
		client := &safeCheckClient{
			code:  map[string]string{realSafe: safeProxyV130RuntimeTest, fakeSafe: "0x6080604052"},
			slot0: map[string]string{realSafe: safeV130SingletonSlotTest, fakeSafe: safeV130SingletonSlotTest},
		}
		idx := newIndexer(client)

		confirmed, failures := idx.checkSafeCandidates(context.Background(), blocks, receipts, nil)
		assert.Empty(t, failures)
		assert.Equal(t, map[string]bool{realSafe: true}, confirmed)

		// A confirmed Safe is cached; only the fake one is looked up again.
		client.calls = 0
		confirmed, _ = idx.checkSafeCandidates(context.Background(), blocks, receipts, nil)
		assert.Equal(t, map[string]bool{realSafe: true}, confirmed)
		assert.Equal(t, 1, client.calls)
	})

	t.Run("a failed lookup fails the block instead of skipping the transfer", func(t *testing.T) {
		idx := newIndexer(&safeCheckClient{err: errors.New("rpc down")})

		confirmed, failures := idx.checkSafeCandidates(context.Background(), blocks, receipts, nil)
		assert.Empty(t, confirmed)
		assert.Contains(t, failures, uint64(1))
		assert.Contains(t, failures, uint64(2))

		results := idx.buildBlockResults([]uint64{1}, blocks, nil, nil, nil, confirmed, failures)
		require.Len(t, results, 1)
		require.NotNil(t, results[0].Error)
	})

	t.Run("traced transactions are not checked", func(t *testing.T) {
		client := &safeCheckClient{}
		idx := newIndexer(client)
		traces := map[string]*evm.CallTrace{"0xreal": {}, "0xfake": {}}

		confirmed, failures := idx.checkSafeCandidates(context.Background(), blocks, receipts, traces)
		assert.Empty(t, confirmed)
		assert.Empty(t, failures)
		assert.Zero(t, client.calls)
	})
}

func TestConvertBlock_TraceOverridesSafeHeuristic(t *testing.T) {
	// When trace is available for a Safe tx, trace is used instead of Safe heuristic
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
	}

	receipt := &evm.TxnReceipt{
		TransactionHash: "0xtx1",
		Status:          "0x1",
		GasUsed:         "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
		Logs: []evm.Log{
			{
				Address: "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
				Topics:  []string{evm.SAFE_EXECUTION_SUCCESS_TOPIC, "0x0"},
			},
		},
	}

	block := &evm.Block{
		Number:    "0x1",
		Timestamp: "0x60000000",
		Transactions: []evm.Txn{
			{
				Hash:  "0xtx1",
				From:  "0xsender",
				To:    "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
				Value: "0x0",
				Input: safeExecInputTest,
			},
		},
	}

	traces := map[string]*evm.CallTrace{
		"0xtx1": {
			From:  "0xsender",
			To:    "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
			Value: "0x0",
			Type:  "CALL",
			Input: safeExecInputTest,
			Calls: []evm.CallTrace{
				{
					From:  "0x84ba2321d46814fb1aa69a7b71882efea50f700c",
					To:    "0xc26dc13d057824342d5480b153f288bd1c5e3e9d",
					Value: "0x16345785d8a0000", // 0.1 ETH
					Type:  "CALL",
				},
			},
		},
	}

	receiptMap := map[string]*evm.TxnReceipt{"0xtx1": receipt}
	result, err := idx.convertBlock(block, receiptMap, traces, nil)
	require.NoError(t, err)

	var nativeCount int
	for _, tx := range result.Transactions {
		if tx.Type == constant.TxTypeNativeTransfer {
			nativeCount++
		}
	}
	// Trace provides the transfer, Safe heuristic is skipped, dedup removes any overlap
	assert.Equal(t, 1, nativeCount, "should have exactly 1 native transfer (trace, no duplicate from Safe)")
}

func TestExtractReceiptTxHashes_TraceInactive_BackwardCompatible(t *testing.T) {
	// With traceActive=false, behavior is identical to original code
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
		// pubkeyStore=nil → full-chain mode
	}

	blocks := map[uint64]*evm.Block{
		1: {
			Transactions: []evm.Txn{
				{Hash: "0x1", From: "0xa", To: "0xb", Value: "0x1", Input: ""},     // native transfer → NeedReceipt=true
				{Hash: "0x2", From: "0xa", To: "0xb", Value: "0x0", Input: "0xab"}, // contract call → NeedReceipt=false
			},
		},
	}

	result := idx.extractReceiptTxHashes(blocks, false)

	// Only native transfer passes NeedReceipt gate
	assert.Equal(t, []string{"0x1"}, result[1])
}

func TestExtractReceiptTxHashes_TraceActive_IncludesContractCalls(t *testing.T) {
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
		// pubkeyStore=nil → full-chain mode
	}

	blocks := map[uint64]*evm.Block{
		1: {
			Transactions: []evm.Txn{
				{Hash: "0x1", From: "0xa", To: "0xb", Value: "0x1", Input: ""},          // native transfer
				{Hash: "0x2", From: "0xa", To: "0xc", Value: "0x0", Input: "0xabcdef00"}, // contract call
				{Hash: "0x3", From: "0xa", To: "0xd", Value: "0x0", Input: "0x"},          // empty input, not contract call
			},
		},
	}

	result := idx.extractReceiptTxHashes(blocks, true)

	// trace active: native transfer (NeedReceipt) + contract call
	// 0x3 has input "0x" which is NOT a contract call and NeedReceipt returns true for it
	assert.Contains(t, result[1], "0x1")
	assert.Contains(t, result[1], "0x2")
	assert.Contains(t, result[1], "0x3") // "0x" input → NeedReceipt=true
}

func TestExtractReceiptTxHashes_TraceActive_NoDuplicateHashes(t *testing.T) {
	// A contract call between two monitored addresses should not be appended twice
	idx := &EVMIndexer{
		chainName: "ethereum",
		config: config.ChainConfig{
			NetworkId:      "eth",
			TwoWayIndexing: true,
		},
		pubkeyStore: evmPubkeyStoreStub{
			addresses: map[string]bool{
				evm.ToChecksumAddress("0xaaaa"): true,
				evm.ToChecksumAddress("0xbbbb"): true,
			},
		},
	}

	blocks := map[uint64]*evm.Block{
		1: {
			Transactions: []evm.Txn{
				{
					Hash:  "0x1",
					From:  "0xaaaa", // monitored
					To:    "0xbbbb", // monitored
					Value: "0x1",
					Input: "0xabcdef00", // contract call
				},
			},
		},
	}

	result := idx.extractReceiptTxHashes(blocks, true)

	// Should appear exactly once despite matching both address filter and trace contract-call branch
	assert.Len(t, result[1], 1)
	assert.Equal(t, "0x1", result[1][0])
}

func TestExtractReceiptTxHashes_PubkeyStoreNil_TraceActive_OnlyContractCallsAndNeedReceipt(t *testing.T) {
	// Full-chain mode + trace active: should NOT fetch receipts for every tx
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
		// pubkeyStore=nil
	}

	blocks := map[uint64]*evm.Block{
		1: {
			Transactions: []evm.Txn{
				{Hash: "0x1", From: "0xa", To: "0xb", Value: "0x1", Input: ""},          // native → NeedReceipt=true
				{Hash: "0x2", From: "0xa", To: "0xc", Value: "0x0", Input: "0xabcd1234"}, // contract call
				{Hash: "0x3", From: "0xa", To: "0xd", Value: "0x0", Input: "0xdeadbeef"}, // another contract call
			},
		},
	}

	result := idx.extractReceiptTxHashes(blocks, true)

	// All 3 should be included: 0x1 via NeedReceipt, 0x2 and 0x3 as contract calls
	assert.Len(t, result[1], 3)
}

func TestTraceModeActive_NilTraceFailover(t *testing.T) {
	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: true},
		traceFailover: nil,
	}
	assert.False(t, idx.traceModeActive(), "should be false when traceFailover is nil")
}

func TestTraceModeActive_DebugTraceDisabled(t *testing.T) {
	idx := &EVMIndexer{
		config:        config.ChainConfig{DebugTrace: false},
		traceFailover: nil,
	}
	assert.False(t, idx.traceModeActive(), "should be false when debug_trace is disabled")
}

func TestBuildBlockResults_WithTraces(t *testing.T) {
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
	}

	txHash := "0xtx1"
	blockNum := uint64(1)

	blocks := map[uint64]*evm.Block{
		blockNum: {
			Number:    "0x1",
			Timestamp: "0x60000000",
			Transactions: []evm.Txn{
				{
					Hash:     txHash,
					From:     "0xaaaa",
					To:       "0xbbbb",
					Value:    "0x0",
					Input:    "0x12345678",
					Gas:      "0x5208",
					GasPrice: "0x3b9aca00",
				},
			},
		},
	}

	receipt := &evm.TxnReceipt{
		TransactionHash:   txHash,
		Status:            "0x1",
		GasUsed:           "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
	}
	allReceipts := map[string]*evm.TxnReceipt{txHash: receipt}
	txHashMap := map[uint64][]string{blockNum: {txHash}}

	traces := map[string]*evm.CallTrace{
		txHash: {
			From:  "0xaaaa",
			To:    "0xbbbb",
			Value: "0x0",
			Type:  "CALL",
			Input: "0x12345678",
			Calls: []evm.CallTrace{
				{
					From:  "0xbbbb",
					To:    "0xcccc",
					Value: "0xde0b6b3a7640000", // 1 ETH
					Type:  "CALL",
				},
			},
		},
	}

	results := idx.buildBlockResults([]uint64{blockNum}, blocks, txHashMap, allReceipts, traces, nil, nil)

	require.Len(t, results, 1)
	assert.Nil(t, results[0].Error)
	require.NotNil(t, results[0].Block)

	// Should contain the internal transfer from trace
	var nativeCount int
	for _, tx := range results[0].Block.Transactions {
		if tx.Type == constant.TxTypeNativeTransfer {
			nativeCount++
		}
	}
	assert.Equal(t, 1, nativeCount, "trace should produce 1 internal native transfer")
}

func TestBuildBlockResults_MissingBlock(t *testing.T) {
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
	}

	// Block 1 exists, block 2 does not
	blocks := map[uint64]*evm.Block{
		1: {
			Number:       "0x1",
			Timestamp:    "0x60000000",
			Transactions: []evm.Txn{},
		},
	}

	results := idx.buildBlockResults([]uint64{1, 2}, blocks, nil, nil, nil, nil, nil)

	require.Len(t, results, 2)
	assert.Nil(t, results[0].Error, "block 1 should succeed")
	assert.NotNil(t, results[0].Block)
	assert.NotNil(t, results[1].Error, "block 2 should have error")
	assert.Equal(t, "block not found", results[1].Error.Message)
}

func TestBuildBlockResults_NilTraces(t *testing.T) {
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
	}

	txHash := "0xtx1"
	blocks := map[uint64]*evm.Block{
		1: {
			Number:    "0x1",
			Timestamp: "0x60000000",
			Transactions: []evm.Txn{
				{
					Hash:     txHash,
					From:     "0xaaaa",
					To:       "0xbbbb",
					Value:    "0xde0b6b3a7640000",
					Input:    "",
					Gas:      "0x5208",
					GasPrice: "0x3b9aca00",
				},
			},
		},
	}

	receipt := &evm.TxnReceipt{
		TransactionHash:   txHash,
		Status:            "0x1",
		GasUsed:           "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
	}

	results := idx.buildBlockResults(
		[]uint64{1},
		blocks,
		map[uint64][]string{1: {txHash}},
		map[string]*evm.TxnReceipt{txHash: receipt},
		nil, // no traces
		nil,
		nil,
	)

	require.Len(t, results, 1)
	assert.Nil(t, results[0].Error)
	require.NotNil(t, results[0].Block)
	// Native transfer from ExtractTransfers should still work
	assert.Len(t, results[0].Block.Transactions, 1)
	assert.Equal(t, constant.TxTypeNativeTransfer, results[0].Block.Transactions[0].Type)
}

func TestConvertBlock_TracesNil_NoNilPanic(t *testing.T) {
	// Verify nil traces map passes through without panic
	idx := &EVMIndexer{
		chainName: "ethereum",
		config:    config.ChainConfig{NetworkId: "eth"},
	}

	block := &evm.Block{
		Number:    "0x1",
		Timestamp: "0x60000000",
		Transactions: []evm.Txn{
			{
				Hash:     "0xtx1",
				From:     "0xaaaa",
				To:       "0xbbbb",
				Value:    "0xde0b6b3a7640000",
				Input:    "",
				Gas:      "0x5208",
				GasPrice: "0x3b9aca00",
			},
		},
	}

	receipt := &evm.TxnReceipt{
		TransactionHash: "0xtx1",
		Status:          "0x1",
		GasUsed:         "0x5208",
		EffectiveGasPrice: "0x3b9aca00",
	}

	result, err := idx.convertBlock(block, map[string]*evm.TxnReceipt{"0xtx1": receipt}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	// Should still extract the native transfer via ExtractTransfers
	assert.Len(t, result.Transactions, 1)
	assert.Equal(t, constant.TxTypeNativeTransfer, result.Transactions[0].Type)
}
