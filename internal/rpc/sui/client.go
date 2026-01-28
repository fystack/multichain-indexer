package sui

import (
	"context"
	"fmt"
	"sync"
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
	conn             *grpc.ClientConn
	ledgerClient     v2.LedgerServiceClient
	subscriberClient v2.SubscriptionServiceClient
	baseURL          string
	network          string
	clientType       string

	// Streaming & Caching
	streamMu           sync.RWMutex
	streamHeight       uint64
	streamBuffer       map[uint64]*Checkpoint
	streamDisconnected bool
}

// NewSuiClient creates a new Sui gRPC client
func NewSuiClient(url string) *SuiClient {
	return &SuiClient{
		baseURL:      url,
		network:      "sui",
		clientType:   "grpc",
		streamBuffer: make(map[uint64]*Checkpoint),
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
	// Initialize subscription client
	c.subscriberClient = v2.NewSubscriptionServiceClient(conn)
	return nil
}

// StartStreaming starts the background streaming process
func (c *SuiClient) StartStreaming(ctx context.Context) error {
	// Call connect to ensure connection exists before streaming
	if err := c.connect(ctx); err != nil {
		return err
	}
	go c.streamLoop(ctx)
	return nil
}

func (c *SuiClient) streamLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := c.subscribe(ctx); err != nil {
				c.streamMu.Lock()
				c.streamDisconnected = true
				c.streamMu.Unlock()
				// Backoff
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func (c *SuiClient) subscribe(ctx context.Context) error {
	mask, err := fieldmaskpb.New(
		&v2.SubscribeCheckpointsResponse{},
		"checkpoint.sequence_number",
		"checkpoint.digest",
		"checkpoint.summary",
		"checkpoint.transactions",
		"checkpoint.transactions.transaction",
		"checkpoint.transactions.effects",
		"checkpoint.transactions.balance_changes",
	)
	if err != nil {
		return err
	}

	req := &v2.SubscribeCheckpointsRequest{
		ReadMask: mask,
	}

	stream, err := c.subscriberClient.SubscribeCheckpoints(ctx, req)
	if err != nil {
		return fmt.Errorf("subscribe failed: %w", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			return err
		}

		cursor := resp.GetCursor()
		checkpoint := resp.GetCheckpoint()

		seq := cursor
		if checkpoint != nil && checkpoint.SequenceNumber != nil {
			seq = *checkpoint.SequenceNumber
		}

		if seq > 0 {
			c.streamMu.Lock()
			c.streamDisconnected = false
			if seq > c.streamHeight {
				c.streamHeight = seq
			}

			if checkpoint != nil && checkpoint.GetDigest() != "" {
				// Ensure SequenceNumber is populated if we relied on cursor
				if checkpoint.SequenceNumber == nil {
					checkpoint.SequenceNumber = &seq
				}

				// Buffer recent checkpoints
				c.streamBuffer[seq] = &Checkpoint{Checkpoint: checkpoint}

				// Prune buffer (limit to 100)
				if len(c.streamBuffer) > 100 {
					for k := range c.streamBuffer {
						if k < seq-100 {
							delete(c.streamBuffer, k)
						}
					}
				}
			}
			c.streamMu.Unlock()
		}
	}
}

// GetLatestCheckpointSequence returns the latest checkpoint sequence number
func (c *SuiClient) GetLatestCheckpointSequence(ctx context.Context) (uint64, error) {
	// Check stream first
	c.streamMu.RLock()
	height := c.streamHeight
	disconnected := c.streamDisconnected
	c.streamMu.RUnlock()

	if height > 0 && !disconnected {
		return height, nil
	}

	if err := c.connect(ctx); err != nil {
		return 0, err
	}

	// Use lightweight GetServiceInfo fallback
	resp, err := c.ledgerClient.GetServiceInfo(ctx, &v2.GetServiceInfoRequest{})
	if err != nil {
		return 0, fmt.Errorf("GetServiceInfo failed (fallback): %w", err)
	}

	return resp.GetCheckpointHeight(), nil
}

// GetCheckpoint returns a checkpoint by sequence number
func (c *SuiClient) GetCheckpoint(ctx context.Context, sequenceNumber uint64) (*Checkpoint, error) {
	// Check stream buffer first
	c.streamMu.RLock()
	cp, found := c.streamBuffer[sequenceNumber]
	c.streamMu.RUnlock()
	if found {
		return cp, nil
	}

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
