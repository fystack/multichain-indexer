package tron

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fystack/transaction-indexer/internal/rpc"
)

type TronClient struct {
	*rpc.Client
}

func NewTronClient(nodes []string, config rpc.ClientConfig) *TronClient {
	return &TronClient{
		Client: rpc.NewClient(nodes, config, func(base string) string {
			return base + "/jsonrpc"
		}),
	}
}
func (c *TronClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	start := time.Now()
	resp, err := c.Client.Call(ctx, method, params)
	duration := time.Since(start)
	slog.Debug("Call duration", "method", method, "params", params, "took", duration)
	return resp, err
}
