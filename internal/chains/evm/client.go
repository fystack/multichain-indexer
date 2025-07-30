package evm

import (
	"context"
	"encoding/json"

	"github.com/fystack/indexer/internal/rpc"
)

type EvmClient struct {
	*rpc.Client
}

func NewEvmClient(nodes []string, config rpc.ClientConfig) *EvmClient {
	return &EvmClient{
		Client: rpc.NewClientWithConfig(nodes, config, func(base string) string {
			return base
		}),
	}
}

func (c *EvmClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.Client.Call(ctx, method, params)
}
