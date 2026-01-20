package sui

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/fystack/multichain-indexer/internal/rpc"
	v2 "github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
)

// SuiClient implements the SuiAPI interface using gRPC
type SuiClient struct {
	conn         *grpc.ClientConn
	ledgerClient v2.LedgerServiceClient
	baseURL      string
	network      string
	clientType   string
}

// NewSuiClient creates a new Sui gRPC client
func NewSuiClient(url string) *SuiClient {
	return &SuiClient{
		baseURL:    url,
		network:    "sui",
		clientType: "grpc",
	}
}

// connect establishes the gRPC connection if not already connected
func (c *SuiClient) connect(ctx context.Context) error {
	// Apply default options
	options := &clientOptions{
		maxMsgSize:  50 * 1024 * 1024, // 50MB
		keepalive:   30 * time.Second,
		retryPolicy: defaultRetryPolicy(),
	}

	// Create TLS credentials
	creds := credentials.NewClientTLSFromCert(nil, "")

	// Configure gRPC dial options
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(options.maxMsgSize),
			grpc.MaxCallSendMsgSize(options.maxMsgSize),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                options.keepalive,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	// Add retry policy if configured
	if options.retryPolicy != nil {
		dialOpts = append(dialOpts, grpc.WithDefaultServiceConfig(*options.retryPolicy))
	}

	// Establish connection
	conn, err := grpc.NewClient(c.baseURL, dialOpts...)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	c.conn = conn
	c.ledgerClient = v2.NewLedgerServiceClient(conn)
	return nil
}

// GetLatestCheckpointSequence returns the latest checkpoint sequence number
func (c *SuiClient) GetLatestCheckpointSequence(ctx context.Context) (uint64, error) {
	if err := c.connect(ctx); err != nil {
		return 0, err
	}

	// Get current checkpoint
	req := &v2.GetCheckpointRequest{
		ReadMask: &fieldmaskpb.FieldMask{
			Paths: []string{
				"sequence_number",
				"summary.timestamp",
				"contents.transactions",
			},
		},
	}

	resp, err := c.ledgerClient.GetCheckpoint(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("GetCheckpoint failed: %w", err)
	}

	if resp.Checkpoint == nil {
		return 0, fmt.Errorf("checkpoint not available")
	}

	return *resp.Checkpoint.SequenceNumber, nil
}

// GetCheckpoint returns a checkpoint by sequence number
func (c *SuiClient) GetCheckpoint(ctx context.Context, sequenceNumber uint64) (*Checkpoint, error) {
	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	req := &v2.GetCheckpointRequest{
		CheckpointId: &v2.GetCheckpointRequest_SequenceNumber{
			SequenceNumber: sequenceNumber,
		},
		ReadMask: &fieldmaskpb.FieldMask{
			Paths: []string{
				"sequence_number",
				"digest",
				"summary",
				"transactions",
				"transactions.transaction",
				"transactions.effects",
				"transactions.balance_changes",
			},
		},
	}

	resp, err := c.ledgerClient.GetCheckpoint(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("GetCheckpoint failed for sequence %d: %w", sequenceNumber, err)
	}

	if resp.Checkpoint == nil {
		return nil, fmt.Errorf("checkpoint %d not found", sequenceNumber)
	}

	return &Checkpoint{Checkpoint: resp.Checkpoint}, nil
}

// BatchGetCheckpoints fetches multiple checkpoints by sequence numbers
func (c *SuiClient) BatchGetCheckpoints(ctx context.Context, sequenceNumbers []uint64) (map[uint64]*Checkpoint, error) {
	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	results := make(map[uint64]*Checkpoint)
	if len(sequenceNumbers) == 0 {
		return results, nil
	}

	// Note: The proto doesn't have BatchGetCheckpoints, so we'll do sequential calls
	for _, seq := range sequenceNumbers {
		cp, err := c.GetCheckpoint(ctx, seq)
		if err != nil {
			continue
		}
		results[seq] = cp
	}

	return results, nil
}

// GetTransaction returns a transaction by digest
func (c *SuiClient) GetTransaction(ctx context.Context, digest string) (*Transaction, error) {
	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	req := &v2.GetTransactionRequest{
		Digest: &digest,
	}

	resp, err := c.ledgerClient.GetTransaction(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("GetTransaction failed for digest %s: %w", digest, err)
	}

	if resp.Transaction == nil {
		return nil, fmt.Errorf("transaction %s not found", digest)
	}

	return &Transaction{ExecutedTransaction: resp.Transaction}, nil
}

// BatchGetTransactions fetches multiple transactions by digests
func (c *SuiClient) BatchGetTransactions(ctx context.Context, digests []string) (map[string]*Transaction, error) {
	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	results := make(map[string]*Transaction)
	if len(digests) == 0 {
		return results, nil
	}

	req := &v2.BatchGetTransactionsRequest{
		Digests: digests,
	}

	resp, err := c.ledgerClient.BatchGetTransactions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("BatchGetTransactions failed: %w", err)
	}

	for i, txResult := range resp.Transactions {
		if i >= len(digests) {
			break
		}
		digest := digests[i]
		if txResult.GetTransaction() != nil {
			results[digest] = &Transaction{ExecutedTransaction: txResult.GetTransaction()}
		}
	}

	return results, nil
}

// NetworkClient interface implementation

// CallRPC is not applicable for gRPC clients
func (c *SuiClient) CallRPC(ctx context.Context, method string, params any) (*rpc.RPCResponse, error) {
	return nil, fmt.Errorf("CallRPC not supported for gRPC client, use gRPC methods instead")
}

// Do is not applicable for gRPC clients
func (c *SuiClient) Do(ctx context.Context, method, endpoint string, body any, params map[string]string) ([]byte, error) {
	return nil, fmt.Errorf("Do not supported for gRPC client, use gRPC methods instead")
}

// GetNetworkType returns the network type
func (c *SuiClient) GetNetworkType() string {
	return c.network
}

// GetClientType returns the client type
func (c *SuiClient) GetClientType() string {
	return c.clientType
}

// GetURL returns the base URL
func (c *SuiClient) GetURL() string {
	return c.baseURL
}

// Close closes the gRPC connection
func (c *SuiClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
