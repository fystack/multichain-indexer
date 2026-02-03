package sui

import (
	v2 "github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
)

// Checkpoint wraps the v2.Checkpoint with convenience methods
type Checkpoint struct {
	*v2.Checkpoint
}

// SequenceNumber returns the checkpoint sequence number
func (c *Checkpoint) SequenceNumber() uint64 {
	if c.Checkpoint == nil || c.Checkpoint.SequenceNumber == nil {
		return 0
	}
	return *c.Checkpoint.SequenceNumber
}

// Digest returns the checkpoint digest
func (c *Checkpoint) Digest() string {
	if c.Checkpoint == nil || c.Checkpoint.Digest == nil {
		return ""
	}
	return *c.Checkpoint.Digest
}

// PreviousDigest returns the previous checkpoint digest
func (c *Checkpoint) PreviousDigest() string {
	if c.Checkpoint == nil || c.Checkpoint.Summary == nil || c.Checkpoint.Summary.PreviousDigest == nil {
		return ""
	}
	return *c.Checkpoint.Summary.PreviousDigest
}

// TimestampMs returns the timestamp in milliseconds
func (c *Checkpoint) TimestampMs() uint64 {
	if c.Checkpoint == nil || c.Checkpoint.Summary == nil || c.Checkpoint.Summary.Timestamp == nil {
		return 0
	}
	ts := c.Checkpoint.Summary.Timestamp
	// Convert protobuf timestamp to milliseconds
	return uint64(ts.Seconds)*1000 + uint64(ts.Nanos)/1_000_000
}

// Transaction wraps the v2.ExecutedTransaction
type Transaction struct {
	*v2.ExecutedTransaction
}
