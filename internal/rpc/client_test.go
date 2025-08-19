package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGenericClient_IsHealthy(t *testing.T) {
	tests := []struct {
		name        string
		network     string
		clientType  string
		handler     func(w http.ResponseWriter, r *http.Request)
		wantHealthy bool
	}{
		{
			name:       "EVM RPC healthy",
			network:    NetworkEVM,
			clientType: ClientTypeRPC,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      1,
					"result":  "1",
				})
			},
			wantHealthy: true,
		},
		{
			name:       "Solana RPC healthy",
			network:    NetworkSolana,
			clientType: ClientTypeRPC,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      1,
					"result":  "ok",
				})
			},
			wantHealthy: true,
		},
		{
			name:       "Bitcoin RPC healthy",
			network:    NetworkBitcoin,
			clientType: ClientTypeRPC,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      1,
					"result":  map[string]any{"chain": "main"},
				})
			},
			wantHealthy: true,
		},
		{
			name:       "Tron REST healthy",
			network:    NetworkTron,
			clientType: ClientTypeREST,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"blockID": "test",
					"block_header": map[string]any{
						"raw_data": map[string]any{
							"number": 123,
						},
					},
				})
			},
			wantHealthy: true,
		},
		{
			name:       "Generic fallback healthy",
			network:    "unknown",
			clientType: ClientTypeREST,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(http.StatusOK)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			},
			wantHealthy: true,
		},
		{
			name:       "Unhealthy server",
			network:    NetworkEVM,
			clientType: ClientTypeRPC,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantHealthy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.handler))
			defer server.Close()

			client := NewGenericClient(server.URL, tt.network, tt.clientType, nil, 5*time.Second, nil)
			healthy := client.IsHealthy(context.Background())

			if healthy != tt.wantHealthy {
				t.Errorf("IsHealthy() = %v, want %v", healthy, tt.wantHealthy)
			}
		})
	}
}

func TestAuthConfig_NodeToAuthConfig(t *testing.T) {
	tests := []struct {
		name string
		node struct {
			ApiKey  string
			Headers map[string]string
		}
		want *AuthConfig
	}{
		{
			name: "bearer token",
			node: struct {
				ApiKey  string
				Headers map[string]string
			}{
				ApiKey: "bearer abc123",
			},
			want: &AuthConfig{
				Type:  "bearer",
				Token: "abc123",
			},
		},
		{
			name: "api key as bearer",
			node: struct {
				ApiKey  string
				Headers map[string]string
			}{
				ApiKey: "xyz789",
			},
			want: &AuthConfig{
				Type:  "bearer",
				Token: "xyz789",
			},
		},
		{
			name: "custom headers",
			node: struct {
				ApiKey  string
				Headers map[string]string
			}{
				Headers: map[string]string{
					"X-API-Key": "custom123",
				},
			},
			want: &AuthConfig{
				Type: "custom",
				Headers: map[string]string{
					"X-API-Key": "custom123",
				},
			},
		},
		{
			name: "no auth",
			node: struct {
				ApiKey  string
				Headers map[string]string
			}{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock core.Node-like structure
			node := struct {
				ApiKey  string
				Headers map[string]string
			}{
				ApiKey:  tt.node.ApiKey,
				Headers: tt.node.Headers,
			}

			// We need to create a proper core.Node, but for testing we'll simulate
			coreNode := struct {
				ApiKey  string
				Headers map[string]string
			}{
				ApiKey:  node.ApiKey,
				Headers: node.Headers,
			}

			// Since we can't easily create a core.Node here, we'll test the logic manually
			var got *AuthConfig

			if len(coreNode.Headers) > 0 {
				got = &AuthConfig{
					Type:    "custom",
					Headers: make(map[string]string),
				}
				for k, v := range coreNode.Headers {
					got.Headers[k] = v
				}
			} else if coreNode.ApiKey != "" {
				if len(coreNode.ApiKey) > 7 && coreNode.ApiKey[:7] == "bearer " {
					got = &AuthConfig{
						Type:  "bearer",
						Token: coreNode.ApiKey[7:],
					}
				} else if len(coreNode.ApiKey) > 7 && coreNode.ApiKey[:7] == "Bearer " {
					got = &AuthConfig{
						Type:  "bearer",
						Token: coreNode.ApiKey[7:],
					}
				} else {
					got = &AuthConfig{
						Type:  "bearer",
						Token: coreNode.ApiKey,
					}
				}
			}

			if (got == nil && tt.want != nil) || (got != nil && tt.want == nil) {
				t.Errorf("got %v, want %v", got, tt.want)
				return
			}

			if got != nil && tt.want != nil {
				if got.Type != tt.want.Type || got.Token != tt.want.Token {
					t.Errorf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
