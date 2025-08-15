package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/internal/common/ratelimiter"
)

// Solana specific types and methods
type SolanaClient struct {
	*GenericClient
}

// NewSolanaClient creates a new Solana client (JSON-RPC)
func NewSolanaClient(url string, auth *AuthConfig, timeout time.Duration) *SolanaClient {
	return &SolanaClient{
		GenericClient: NewGenericClient(url, NetworkSolana, ClientTypeRPC, auth, timeout, nil),
	}
}

// GetSlot returns the current slot
func (s *SolanaClient) GetSlot(ctx context.Context) (uint64, error) {
	resp, err := s.CallRPC(ctx, "getSlot", nil)
	if err != nil {
		return 0, fmt.Errorf("getSlot failed: %w", err)
	}

	var slot uint64
	if err := json.Unmarshal(resp.Result, &slot); err != nil {
		return 0, fmt.Errorf("failed to unmarshal slot: %w", err)
	}
	return slot, nil
}

// SolanaBalanceResponse represents the response of getBalance
type SolanaBalanceResponse struct {
	Context struct {
		Slot uint64 `json:"slot"`
	} `json:"context"`
	Value uint64 `json:"value"`
}

// GetBalance returns account balance in lamports
func (s *SolanaClient) GetBalance(ctx context.Context, address string) (uint64, error) {
	resp, err := s.CallRPC(ctx, "getBalance", []any{address})
	if err != nil {
		return 0, fmt.Errorf("getBalance failed: %w", err)
	}
	var br SolanaBalanceResponse
	if err := json.Unmarshal(resp.Result, &br); err != nil {
		return 0, fmt.Errorf("failed to unmarshal balance: %w", err)
	}
	return br.Value, nil
}

// SolanaBlock represents a minimal Solana block view
type SolanaBlock struct {
	Blockhash         string `json:"blockhash"`
	PreviousBlockhash string `json:"previousBlockhash"`
	ParentSlot        uint64 `json:"parentSlot"`
}

// GetBlock returns a block by slot
func (s *SolanaClient) GetBlock(ctx context.Context, slot uint64) (*SolanaBlock, error) {
	resp, err := s.CallRPC(ctx, "getBlock", []any{slot})
	if err != nil {
		return nil, fmt.Errorf("getBlock failed: %w", err)
	}
	var block SolanaBlock
	if err := json.Unmarshal(resp.Result, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &block, nil
}

// SolanaTransaction represents a minimal Solana transaction view
type SolanaTransaction struct {
	Slot      uint64 `json:"slot"`
	Signature string `json:"signature"`
}

// GetTransaction returns a transaction by signature
func (s *SolanaClient) GetTransaction(ctx context.Context, signature string) (*SolanaTransaction, error) {
	resp, err := s.CallRPC(ctx, "getTransaction", []any{signature})
	if err != nil {
		return nil, fmt.Errorf("getTransaction failed: %w", err)
	}
	var tx SolanaTransaction
	if err := json.Unmarshal(resp.Result, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}
	return &tx, nil
}

// SendTransaction broadcasts a base64-encoded signed transaction
func (s *SolanaClient) SendTransaction(ctx context.Context, base64Tx string) (string, error) {
	resp, err := s.CallRPC(ctx, "sendTransaction", []any{base64Tx})
	if err != nil {
		return "", fmt.Errorf("sendTransaction failed: %w", err)
	}
	var sig string
	if err := json.Unmarshal(resp.Result, &sig); err != nil {
		return "", fmt.Errorf("failed to unmarshal signature: %w", err)
	}
	return sig, nil
}

// FailoverManager helpers for Solana
// AddSolanaProvider adds a Solana provider to the failover manager
func (fm *FailoverManager) AddSolanaProvider(name, url string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	return fm.AddProvider(name, url, NetworkSolana, ClientTypeRPC, auth, rateLimiter)
}

// GetSolanaClient returns the current provider as a Solana client
func (fm *FailoverManager) GetSolanaClient() (*SolanaClient, error) {
	provider, err := fm.GetBestProvider()
	if err != nil {
		return nil, err
	}
	if provider.Network != NetworkSolana {
		return nil, fmt.Errorf("current provider is not a Solana network")
	}
	genericClient := provider.Client.(*GenericClient)
	return &SolanaClient{GenericClient: genericClient}, nil
}

// ExecuteSolanaCall executes a function with a Solana client and automatic failover
func (fm *FailoverManager) ExecuteSolanaCall(ctx context.Context, fn func(*SolanaClient) error) error {
	return fm.ExecuteWithRetry(ctx, func(client NetworkClient) error {
		if client.GetNetworkType() != NetworkSolana {
			return fmt.Errorf("expected Solana client, got %s", client.GetNetworkType())
		}
		solClient := &SolanaClient{GenericClient: client.(*GenericClient)}
		return fn(solClient)
	})
}
