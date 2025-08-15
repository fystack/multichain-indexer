package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/transaction-indexer/internal/common/ratelimiter"
)

type EthereumClient struct {
	*GenericClient
}

func NewEthereumClient(url string, auth *AuthConfig, timeout time.Duration, rateLimiter *ratelimiter.PooledRateLimiter) *EthereumClient {
	return &EthereumClient{
		GenericClient: NewGenericClient(url, NetworkEVM, ClientTypeRPC, auth, timeout, rateLimiter),
	}
}

type (
	EthBlock struct {
		Number       string           `json:"number"`
		Hash         string           `json:"hash"`
		ParentHash   string           `json:"parentHash"`
		Timestamp    string           `json:"timestamp"`
		Transactions []EthTransaction `json:"transactions"`
	}

	EthTransaction struct {
		Hash        string `json:"hash"`
		From        string `json:"from"`
		To          string `json:"to"`
		Value       string `json:"value"`
		Input       string `json:"input"`
		Gas         string `json:"gas"`
		GasPrice    string `json:"gasPrice"`
		BlockNumber string `json:"blockNumber"`
	}

	EthTransactionReceipt struct {
		TransactionHash   string   `json:"transactionHash"`
		GasUsed           string   `json:"gasUsed"`
		EffectiveGasPrice string   `json:"effectiveGasPrice"`
		Logs              []EthLog `json:"logs"`
	}

	EthLog struct {
		Address         string   `json:"address"`
		Topics          []string `json:"topics"`
		Data            string   `json:"data"`
		BlockNumber     string   `json:"blockNumber"`
		TransactionHash string   `json:"transactionHash"`
		LogIndex        string   `json:"logIndex"`
	}
)

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

// GetBlockByNumber returns a block with full transaction data
func (e *EthereumClient) GetBlockByNumber(ctx context.Context, blockNumber string, detail bool) (*EthBlock, error) {
	if blockNumber == "" {
		blockNumber = "latest"
	}

	resp, err := e.CallRPC(ctx, "eth_getBlockByNumber", []any{blockNumber, detail})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	var block EthBlock
	if err := json.Unmarshal(resp.Result, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &block, nil
}

// BatchGetBlocksByNumber gets multiple blocks efficiently
func (e *EthereumClient) BatchGetBlocksByNumber(ctx context.Context, blockNumbers []uint64, fullTxs bool) (map[uint64]*EthBlock, error) {
	results := make(map[uint64]*EthBlock)
	if len(blockNumbers) == 0 {
		return results, nil
	}

	if e.rateLimiter != nil {
		if err := e.rateLimiter.Wait(ctx, e.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit wait failed: %w", err)
		}
	}

	e.mutex.Lock()
	startID := e.rpcID
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
	e.rpcID += int64(len(blockNumbers))
	e.mutex.Unlock()

	resp, err := e.Post(ctx, "/", requests)
	if err != nil {
		return nil, fmt.Errorf("failed to post batch request: %w", err)
	}

	var rpcResponses []RPCResponse
	if err := json.Unmarshal(resp, &rpcResponses); err != nil {
		return nil, fmt.Errorf("failed to unmarshal batch response: %w", err)
	}

	for _, r := range rpcResponses {
		if r.Error != nil {
			slog.Error("batch get blocks failed", "error", r.Error)
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

// BatchGetTransactionReceipts gets multiple transaction receipts for gas fee calculation
func (e *EthereumClient) BatchGetTransactionReceipts(ctx context.Context, txHashes []string) (map[string]*EthTransactionReceipt, error) {
	results := make(map[string]*EthTransactionReceipt)
	if len(txHashes) == 0 {
		return results, nil
	}

	if e.rateLimiter != nil {
		if err := e.rateLimiter.Wait(ctx, e.baseURL); err != nil {
			return nil, fmt.Errorf("rate limit wait failed: %w", err)
		}
	}

	e.mutex.Lock()
	startID := e.rpcID
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
	e.rpcID += int64(len(txHashes))
	e.mutex.Unlock()

	resp, err := e.Post(ctx, "/", requests)
	if err != nil {
		return nil, fmt.Errorf("failed to post batch request: %w", err)
	}

	var rpcResponses []RPCResponse
	if err := json.Unmarshal(resp, &rpcResponses); err != nil {
		return nil, fmt.Errorf("failed to unmarshal batch response: %w", err)
	}

	for _, r := range rpcResponses {
		if r.Error != nil {
			slog.Error("batch get transaction receipts failed", "error", r.Error)
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

// Failover manager helpers
func (fm *FailoverManager) AddEthereumProvider(name, url string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	return fm.AddProvider(name, url, NetworkEVM, ClientTypeRPC, auth, rateLimiter)
}

func (fm *FailoverManager) GetEthereumClient() (*EthereumClient, error) {
	provider, err := fm.GetBestProvider()
	if err != nil {
		return nil, err
	}

	if provider.Network != NetworkEVM {
		return nil, fmt.Errorf("current provider is not an Ethereum network")
	}

	genericClient := provider.Client.(*GenericClient)
	return &EthereumClient{GenericClient: genericClient}, nil
}

func (fm *FailoverManager) ExecuteEthereumCall(ctx context.Context, fn func(*EthereumClient) error) error {
	return fm.ExecuteWithRetry(ctx, func(client NetworkClient) error {
		if client.GetNetworkType() != NetworkEVM {
			return fmt.Errorf("expected Ethereum client, got %s", client.GetNetworkType())
		}

		ethClient := &EthereumClient{GenericClient: client.(*GenericClient)}
		return fn(ethClient)
	})
}
