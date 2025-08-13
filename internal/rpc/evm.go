package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"idx/internal/common/ratelimiter"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Ethereum specific types and methods
type EthereumClient struct {
	*GenericClient
}

// NewEthereumClient creates a new Ethereum client
func NewEthereumClient(url string, auth *AuthConfig, timeout time.Duration, rateLimiter *ratelimiter.PooledRateLimiter) *EthereumClient {
	return &EthereumClient{
		GenericClient: NewGenericClient(url, NetworkEVM, ClientTypeRPC, auth, timeout, rateLimiter),
	}
}

// Ethereum RPC Methods
// GetBlockNumber returns the current block number
func (e *EthereumClient) GetBlockNumber(ctx context.Context) (uint64, error) {
	resp, err := e.CallRPC(ctx, "eth_blockNumber", nil)
	if err != nil {
		return 0, fmt.Errorf("eth_blockNumber failed: %w", err)
	}

	var blockHex string
	if err := json.Unmarshal(resp.Result, &blockHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block number: %w", err)
	}

	blockHex = strings.TrimPrefix(blockHex, "0x")
	blockNum, err := strconv.ParseUint(blockHex, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse block number: %w", err)
	}

	return blockNum, nil
}

// EthBlock represents an Ethereum block
type EthBlock struct {
	Number           string           `json:"number"`
	Hash             string           `json:"hash"`
	ParentHash       string           `json:"parentHash"`
	Nonce            string           `json:"nonce"`
	Sha3Uncles       string           `json:"sha3Uncles"`
	LogsBloom        string           `json:"logsBloom"`
	TransactionsRoot string           `json:"transactionsRoot"`
	StateRoot        string           `json:"stateRoot"`
	ReceiptsRoot     string           `json:"receiptsRoot"`
	Miner            string           `json:"miner"`
	Difficulty       string           `json:"difficulty"`
	TotalDifficulty  string           `json:"totalDifficulty"`
	ExtraData        string           `json:"extraData"`
	Size             string           `json:"size"`
	GasLimit         string           `json:"gasLimit"`
	GasUsed          string           `json:"gasUsed"`
	Timestamp        string           `json:"timestamp"`
	Transactions     []EthTransaction `json:"transactions"`
	Uncles           []string         `json:"uncles"`
}

// GetBlockByNumber returns a block by its number
func (e *EthereumClient) GetBlockByNumber(ctx context.Context, blockNumber string, fullTx bool) (*EthBlock, error) {
	if blockNumber == "" {
		blockNumber = "latest"
	}

	resp, err := e.CallRPC(ctx, "eth_getBlockByNumber", []any{blockNumber, fullTx})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	var block EthBlock
	if err := json.Unmarshal(resp.Result, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &block, nil
}

// EthTransaction represents an Ethereum transaction
type EthTransaction struct {
	Hash             string `json:"hash"`
	Nonce            string `json:"nonce"`
	BlockHash        string `json:"blockHash"`
	BlockNumber      string `json:"blockNumber"`
	TransactionIndex string `json:"transactionIndex"`
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`
	Gas              string `json:"gas"`
	GasPrice         string `json:"gasPrice"`
	Input            string `json:"input"`
}

// EthLog represents an Ethereum log entry
type EthLog struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber,omitempty"`
	TransactionHash  string   `json:"transactionHash,omitempty"`
	TransactionIndex string   `json:"transactionIndex,omitempty"`
	BlockHash        string   `json:"blockHash,omitempty"`
	LogIndex         string   `json:"logIndex,omitempty"`
	Removed          bool     `json:"removed,omitempty"`
}

// EthTransactionReceipt represents an Ethereum transaction receipt
type EthTransactionReceipt struct {
	TransactionHash   string   `json:"transactionHash"`
	TransactionIndex  string   `json:"transactionIndex"`
	BlockHash         string   `json:"blockHash"`
	BlockNumber       string   `json:"blockNumber"`
	From              string   `json:"from"`
	To                string   `json:"to"`
	CumulativeGasUsed string   `json:"cumulativeGasUsed"`
	GasUsed           string   `json:"gasUsed"`
	ContractAddress   string   `json:"contractAddress"`
	Logs              []EthLog `json:"logs"`
	Status            string   `json:"status"`
	EffectiveGasPrice string   `json:"effectiveGasPrice"`
}

// GetTransactionByHash returns a transaction by its hash
func (e *EthereumClient) GetTransactionByHash(ctx context.Context, txHash string) (*EthTransaction, error) {
	resp, err := e.CallRPC(ctx, "eth_getTransactionByHash", []any{txHash})
	if err != nil {
		return nil, fmt.Errorf("eth_getTransactionByHash failed: %w", err)
	}

	var tx EthTransaction
	if err := json.Unmarshal(resp.Result, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}

	return &tx, nil
}

// BatchGetTransactionReceipts gets multiple transaction receipts in a single batch call
func (c *EthereumClient) BatchGetTransactionReceipts(ctx context.Context, txHashes []string) (map[string]*EthTransactionReceipt, error) {
	results := make(map[string]*EthTransactionReceipt)
	if len(txHashes) == 0 {
		return results, nil
	}

	// Apply rate limiting
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, c.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit wait failed: %w", err)
		}
	}

	// Generate request IDs and build batch
	c.mutex.Lock()
	startID := c.rpcID
	requests := make([]*RPCRequest, 0, len(txHashes))
	idToHash := make(map[string]string, len(txHashes))
	for i, h := range txHashes {
		id := startID + int64(i)
		requests = append(requests, &RPCRequest{
			ID:      id,
			JSONRPC: "2.0",
			Method:  "eth_getTransactionReceipt",
			Params:  []any{h},
		})
		idToHash[fmt.Sprint(id)] = h
	}
	c.rpcID += int64(len(txHashes))
	c.mutex.Unlock()

	resp, err := c.Post(ctx, "/", requests)
	if err != nil {
		return nil, fmt.Errorf("failed to post batch request: %w", err)
	}
	var rpcResponses []RPCResponse
	if err := json.Unmarshal(resp, &rpcResponses); err != nil {
		return nil, fmt.Errorf("failed to unmarshal batch RPC response: %w", err)
	}
	for _, r := range rpcResponses {
		if r.Error != nil {
			slog.Error("batch get transaction receipts failed", "error", r.Error)
			// Skip errored entries; caller can decide how to handle missing ones
			continue
		}
		hash, ok := idToHash[fmt.Sprint(r.ID)]
		if !ok {
			continue
		}
		if len(r.Result) == 0 || string(r.Result) == "null" {
			continue
		}
		var receipt EthTransactionReceipt
		if err := json.Unmarshal(r.Result, &receipt); err != nil {
			return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
		}
		results[hash] = &receipt
	}

	return results, nil
}

// BatchGetBlocksByNumber gets multiple blocks by their numbers in batch calls
func (c *EthereumClient) BatchGetBlocksByNumber(ctx context.Context, blockNumbers []uint64, fullTxs bool) (map[uint64]*EthBlock, error) {
	results := make(map[uint64]*EthBlock)
	if len(blockNumbers) == 0 {
		return results, nil
	}

	// Apply rate limiting
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, c.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit wait failed: %w", err)
		}
	}

	// Generate request IDs and build batch
	c.mutex.Lock()
	startID := c.rpcID
	requests := make([]*RPCRequest, 0, len(blockNumbers))
	idToBlockNum := make(map[string]uint64, len(blockNumbers))
	for i, n := range blockNumbers {
		id := startID + int64(i)
		hexNum := fmt.Sprintf("0x%x", n)
		requests = append(requests, &RPCRequest{
			ID:      id,
			JSONRPC: "2.0",
			Method:  "eth_getBlockByNumber",
			Params:  []any{hexNum, fullTxs},
		})
		idToBlockNum[fmt.Sprint(id)] = n
	}
	c.rpcID += int64(len(blockNumbers))
	c.mutex.Unlock()

	resp, err := c.Post(ctx, "/", requests)
	if err != nil {
		return nil, fmt.Errorf("failed to post batch request: %w", err)
	}
	var rpcResponses []RPCResponse
	if err := json.Unmarshal(resp, &rpcResponses); err != nil {
		return nil, fmt.Errorf("failed to unmarshal batch RPC response: %w", err)
	}
	for _, r := range rpcResponses {
		if r.Error != nil {
			slog.Error("batch get blocks failed", "error", r.Error)
			// Skip errored entries
			continue
		}
		if len(r.Result) == 0 || string(r.Result) == "null" {
			continue
		}
		n, ok := idToBlockNum[fmt.Sprint(r.ID)]
		if !ok {
			continue
		}
		var block EthBlock
		if err := json.Unmarshal(r.Result, &block); err != nil {
			return nil, fmt.Errorf("failed to unmarshal block: %w", err)
		}
		results[n] = &block
	}

	return results, nil
}

// FailoverManager helpers for Ethereum
// AddEthereumProvider adds an Ethereum provider to the failover manager
func (fm *FailoverManager) AddEthereumProvider(name, url string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	return fm.AddProvider(name, url, NetworkEVM, ClientTypeRPC, auth, rateLimiter)
}

// GetEthereumClient returns the current provider as an Ethereum client
func (fm *FailoverManager) GetEthereumClient() (*EthereumClient, error) {
	provider, err := fm.GetBestProvider()
	if err != nil {
		return nil, err
	}

	if provider.Network != NetworkEVM {
		return nil, fmt.Errorf("current provider is not an Ethereum network")
	}

	// Convert generic client to Ethereum client
	genericClient := provider.Client.(*GenericClient)
	return &EthereumClient{GenericClient: genericClient}, nil
}

// ExecuteEthereumCall executes a function with an Ethereum client and automatic failover
func (fm *FailoverManager) ExecuteEthereumCall(ctx context.Context, fn func(*EthereumClient) error) error {
	return fm.ExecuteWithRetry(ctx, func(client NetworkClient) error {
		if client.GetNetworkType() != NetworkEVM {
			return fmt.Errorf("expected Ethereum client, got %s", client.GetNetworkType())
		}

		ethClient := &EthereumClient{GenericClient: client.(*GenericClient)}
		return fn(ethClient)
	})
}
