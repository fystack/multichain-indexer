package tron

import (
	"context"
	"encoding/json"

	"github.com/fystack/indexer/internal/rpc"
)

type TronClient struct {
	*rpc.Client
}

func NewTronClient(nodes []string, config rpc.ClientConfig) *TronClient {
	return &TronClient{
		Client: rpc.NewClientWithConfig(nodes, config, func(base string) string {
			return base + "/jsonrpc"
		}),
	}
}
func (c *TronClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.Client.Call(ctx, method, params)
}
