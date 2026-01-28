package sui

import (
	"context"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

// SuiAPI defines the Sui gRPC interface
type SuiAPI interface {
	rpc.NetworkClient

	// Get latest checkpoint sequence number
	GetLatestCheckpointSequence(ctx context.Context) (uint64, error)

	// Get checkpoint by sequence number
	GetCheckpoint(ctx context.Context, sequenceNumber uint64) (*Checkpoint, error)

	// Batch get checkpoints by sequence numbers
	BatchGetCheckpoints(ctx context.Context, sequenceNumbers []uint64) (map[uint64]*Checkpoint, error)

	// Get transaction by digest
	GetTransaction(ctx context.Context, digest string) (*Transaction, error)

	// Batch get transactions by digests
	BatchGetTransactions(ctx context.Context, digests []string) (map[string]*Transaction, error)

	// Start streaming checkpoints
	StartStreaming(ctx context.Context) error
}
