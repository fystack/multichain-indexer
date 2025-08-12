package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"idx/internal/common/ratelimiter"
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
