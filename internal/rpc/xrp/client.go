package xrp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
)

type Client struct {
	*rpc.BaseClient
}

func NewClient(
	url string,
	auth *rpc.AuthConfig,
	timeout time.Duration,
	rateLimiter *ratelimiter.PooledRateLimiter,
) *Client {
	return &Client{
		BaseClient: rpc.NewBaseClient(
			url,
			rpc.NetworkXRP,
			rpc.ClientTypeRPC,
			auth,
			timeout,
			rateLimiter,
		),
	}
}

func (c *Client) GetLatestLedgerIndex(ctx context.Context) (uint64, error) {
	resp, err := c.CallRPC(ctx, "ledger_current", nil)
	if err != nil {
		return 0, fmt.Errorf("ledger_current failed: %w", err)
	}

	var result struct {
		LedgerCurrentIndex uint64 `json:"ledger_current_index"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return 0, fmt.Errorf("decode ledger_current result: %w", err)
	}
	if result.LedgerCurrentIndex == 0 {
		return 0, fmt.Errorf("ledger_current returned empty ledger_current_index")
	}
	return result.LedgerCurrentIndex, nil
}

func (c *Client) GetLedgerByIndex(ctx context.Context, ledgerIndex uint64) (*Ledger, error) {
	resp, err := c.CallRPC(ctx, "ledger", []any{map[string]any{
		"ledger_index": ledgerIndex,
		"transactions": true,
		"expand":       true,
	}})
	if err != nil {
		return nil, fmt.Errorf("ledger %d failed: %w", ledgerIndex, err)
	}

	var result ledgerResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("decode ledger %d result: %w", ledgerIndex, err)
	}
	if result.Error != "" {
		if result.ErrorMessage != "" {
			return nil, fmt.Errorf("ledger %d failed: %s: %s", ledgerIndex, result.Error, result.ErrorMessage)
		}
		return nil, fmt.Errorf("ledger %d failed: %s", ledgerIndex, result.Error)
	}
	if result.Ledger == nil {
		return nil, fmt.Errorf("ledger %d not found", ledgerIndex)
	}
	return result.Ledger, nil
}

func (c *Client) BatchGetLedgersByIndex(ctx context.Context, ledgerIndexes []uint64) (map[uint64]*Ledger, error) {
	if len(ledgerIndexes) == 0 {
		return map[uint64]*Ledger{}, nil
	}

	requests := make([]*rpc.RPCRequest, 0, len(ledgerIndexes))
	idToLedger := make(map[int64]uint64, len(ledgerIndexes))
	for _, ledgerIndex := range ledgerIndexes {
		id := c.NextRequestIDs(1)[0]
		idToLedger[id] = ledgerIndex
		requests = append(requests, &rpc.RPCRequest{
			ID:      id,
			JSONRPC: "2.0",
			Method:  "ledger",
			Params: []any{map[string]any{
				"ledger_index": ledgerIndex,
				"transactions": true,
				"expand":       true,
			}},
		})
	}

	responses, err := c.DoBatch(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("batch ledger fetch failed: %w", err)
	}

	results := make(map[uint64]*Ledger, len(ledgerIndexes))
	for _, response := range responses {
		id, ok := response.IDInt64()
		if !ok {
			return nil, fmt.Errorf("ledger batch returned non-integer id: %v", response.ID)
		}
		ledgerIndex, ok := idToLedger[id]
		if !ok {
			return nil, fmt.Errorf("ledger batch returned unknown id: %d", id)
		}
		if response.Error != nil {
			return nil, fmt.Errorf("ledger %d failed: %w", ledgerIndex, response.Error)
		}

		var result ledgerResponse
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return nil, fmt.Errorf("decode ledger %d result: %w", ledgerIndex, err)
		}
		if result.Error != "" {
			if result.ErrorMessage != "" {
				return nil, fmt.Errorf("ledger %d failed: %s: %s", ledgerIndex, result.Error, result.ErrorMessage)
			}
			return nil, fmt.Errorf("ledger %d failed: %s", ledgerIndex, result.Error)
		}
		if result.Ledger == nil {
			return nil, fmt.Errorf("ledger %d not found", ledgerIndex)
		}
		results[ledgerIndex] = result.Ledger
	}

	return results, nil
}
