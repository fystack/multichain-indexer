package evm

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fystack/indexer/internal/rpc"
)

type EvmClient struct {
	*rpc.Client
}

func NewEvmClient(nodes []string, config rpc.ClientConfig) *EvmClient {
	return &EvmClient{
		Client: rpc.NewClient(nodes, config, func(base string) string {
			return base
		}),
	}
}

func (c *EvmClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	start := time.Now()
	resp, err := c.Client.Call(ctx, method, params)
	duration := time.Since(start)
	slog.Debug("Call duration", "method", method, "params", params, "took", duration)
	return resp, err
}
