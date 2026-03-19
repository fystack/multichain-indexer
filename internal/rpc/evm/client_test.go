package evm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDebugTraceTransaction_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"from":"0xaaaa","to":"0xbbbb","value":"0xde0b6b3a7640000","type":"CALL","input":"0x6a761202","output":"0x","gas":"0x5208","gasUsed":"0x5208","calls":[{"from":"0xbbbb","to":"0xcccc","value":"0x16345785d8a0000","type":"CALL","input":"0x","output":"0x","gas":"0x1000","gasUsed":"0x800"}]},"id":1}`)
	}))
	defer server.Close()

	c := NewEthereumClient(server.URL, nil, 5*time.Second, nil)
	trace, err := c.DebugTraceTransaction(context.Background(), "0xdeadbeef")

	require.NoError(t, err)
	require.NotNil(t, trace)
	assert.Equal(t, "CALL", trace.Type)
	assert.Equal(t, "0xaaaa", trace.From)
	assert.Equal(t, "0xbbbb", trace.To)
	assert.Equal(t, "0xde0b6b3a7640000", trace.Value)
	assert.Equal(t, "0x6a761202", trace.Input)
	assert.Empty(t, trace.Error)

	require.Len(t, trace.Calls, 1)
	assert.Equal(t, "0xbbbb", trace.Calls[0].From)
	assert.Equal(t, "0xcccc", trace.Calls[0].To)
	assert.Equal(t, "0x16345785d8a0000", trace.Calls[0].Value)
}

func TestDebugTraceTransaction_RPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32601,"message":"the method debug_traceTransaction does not exist/is not available"},"id":1}`)
	}))
	defer server.Close()

	c := NewEthereumClient(server.URL, nil, 5*time.Second, nil)
	_, err := c.DebugTraceTransaction(context.Background(), "0xdeadbeef")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "debug_traceTransaction")
	assert.Contains(t, err.Error(), "-32601")
}

func TestDebugTraceTransaction_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `not valid json`)
	}))
	defer server.Close()

	c := NewEthereumClient(server.URL, nil, 5*time.Second, nil)
	_, err := c.DebugTraceTransaction(context.Background(), "0xdeadbeef")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode error")
}
