package sui

import (
	"context"
	"testing"

	"google.golang.org/grpc/encoding"
	grpcgzip "google.golang.org/grpc/encoding/gzip"

	"github.com/stretchr/testify/require"
)

func TestSuiClientRegistersGzipCompressor(t *testing.T) {
	t.Parallel()

	require.NotNil(t, encoding.GetCompressor(grpcgzip.Name))
}

func TestSuiClientConnectReusesExistingConnection(t *testing.T) {
	t.Parallel()

	client := NewSuiClient("passthrough:///127.0.0.1:12345")

	require.NoError(t, client.connect(context.Background()))
	firstConn := client.conn
	require.NotNil(t, firstConn)
	require.NotNil(t, client.ledgerClient)
	require.NotNil(t, client.subscriberClient)

	require.NoError(t, client.connect(context.Background()))
	require.Same(t, firstConn, client.conn)

	require.NoError(t, client.Close())
	require.Nil(t, client.conn)
	require.Nil(t, client.ledgerClient)
	require.Nil(t, client.subscriberClient)

	require.NoError(t, client.connect(context.Background()))
	require.NotNil(t, client.conn)
	require.NotSame(t, firstConn, client.conn)
	require.NoError(t, client.Close())
}
