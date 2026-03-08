package aptos

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetLatestBlockHeight(t *testing.T) {
	tests := []struct {
		name    string
		baseURL func(serverURL string) string
	}{
		{
			name: "base url without v1",
			baseURL: func(serverURL string) string {
				return serverURL
			},
		},
		{
			name: "base url with v1 suffix",
			baseURL: func(serverURL string) string {
				return serverURL + "/v1"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"chain_id":1,"block_height":"12345"}`))
			}))
			defer server.Close()

			client := NewAptosClient(tt.baseURL(server.URL), nil, 5*time.Second, nil)
			height, err := client.GetLatestBlockHeight(context.Background())
			require.NoError(t, err)
			assert.Equal(t, uint64(12345), height)
		})
	}
}

func TestIsNotFoundError(t *testing.T) {
	assert.True(t, IsNotFoundError(errors.New("HTTP 404: not found")))
	assert.True(t, IsNotFoundError(errors.New("resource not found")))
	assert.False(t, IsNotFoundError(nil))
	assert.False(t, IsNotFoundError(context.Canceled))
}
