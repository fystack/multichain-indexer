package solana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransactionVersionRequests(t *testing.T) {
	for _, method := range []string{"getBlock", "getTransaction"} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, method, request.Method)
				if !assert.Len(t, request.Params, 2) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var config map[string]any
				assert.NoError(t, json.Unmarshal(request.Params[1], &config))
				// Check the wire value independently of the production constant.
				if config["maxSupportedTransactionVersion"] != float64(1) {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"jsonrpc": "2.0", "id": 1,
						"error": map[string]any{"code": -32015, "message": "Transaction version (1) is not supported"},
					})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"slot":42,"blockhash":"block","transactions":[],"transaction":{"signatures":["sig"],"message":{"accountKeys":[],"instructions":[],"transactionConfig":{"computeUnitLimit":30000}}},"version":1,"meta":{"fee":5000}}}`))
			}))
			defer server.Close()
			client := NewSolanaClient(server.URL, nil, 5*time.Second, nil)
			defer client.Close()
			if method == "getBlock" {
				block, err := client.GetBlock(context.Background(), 42)
				require.NoError(t, err)
				require.NotNil(t, block)
				assert.Equal(t, "block", block.Blockhash)
			} else {
				tx, err := client.GetTransaction(context.Background(), "sig")
				require.NoError(t, err)
				require.NotNil(t, tx)
				assert.Equal(t, uint64(5000), tx.Meta.Fee)
			}
		})
	}
}
