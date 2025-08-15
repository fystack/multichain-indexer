package kvstore

import (
	"encoding/json"
	"fmt"
	"time"
)

// FailedBlockInfo represents information about a failed block
type FailedBlockInfo struct {
	BlockNumber uint64    `json:"block_number"`
	Chain       string    `json:"chain"`
	Error       string    `json:"error"`
	Timestamp   time.Time `json:"timestamp"`
	RetryCount  int       `json:"retry_count"`
	Resolved    bool      `json:"resolved"`
}

// FailedBlockStore manages failed blocks using KVStore
type FailedBlockStore struct {
	kvstore KVStore
}

// NewFailedBlockStore creates a new FailedBlockStore
func NewFailedBlockStore(kvstore KVStore) *FailedBlockStore {
	return &FailedBlockStore{
		kvstore: kvstore,
	}
}

// StoreFailedBlock stores a failed block with its error information
func (fbs *FailedBlockStore) StoreFailedBlock(chain string, blockNumber uint64, err error) error {
	key := fbs.getBlockKey(chain, blockNumber)

	// Check if block already exists to preserve retry count
	existingInfo, getErr := fbs.GetFailedBlock(chain, blockNumber)
	retryCount := 0
	if getErr == nil && existingInfo != nil {
		retryCount = existingInfo.RetryCount
	}

	info := &FailedBlockInfo{
		BlockNumber: blockNumber,
		Chain:       chain,
		Error:       err.Error(),
		Timestamp:   time.Now(),
		RetryCount:  retryCount + 1,
		Resolved:    false,
	}

	data, marshalErr := json.Marshal(info)
	if marshalErr != nil {
		return fmt.Errorf("failed to marshal failed block info: %w", marshalErr)
	}

	if setErr := fbs.kvstore.Set(key, data); setErr != nil {
		return fmt.Errorf("failed to store failed block: %w", setErr)
	}

	// Also add to chain's failed blocks list
	return fbs.addToFailedBlocksList(chain, blockNumber)
}

// GetFailedBlock retrieves information about a specific failed block
func (fbs *FailedBlockStore) GetFailedBlock(chain string, blockNumber uint64) (*FailedBlockInfo, error) {
	key := fbs.getBlockKey(chain, blockNumber)

	data, err := fbs.kvstore.Get(key)
	if err != nil {
		return nil, err
	}

	var info FailedBlockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal failed block info: %w", err)
	}

	return &info, nil
}

// GetFailedBlocks retrieves all unresolved failed block numbers for a chain
func (fbs *FailedBlockStore) GetFailedBlocks(chain string) ([]uint64, error) {
	key := fbs.getFailedBlocksListKey(chain)

	data, err := fbs.kvstore.Get(key)
	if err != nil {
		// If no failed blocks list exists, return empty slice
		return []uint64{}, nil
	}

	var blockNumbers []uint64
	if err := json.Unmarshal(data, &blockNumbers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal failed blocks list: %w", err)
	}

	// Filter out resolved blocks
	var unresolvedBlocks []uint64
	for _, blockNumber := range blockNumbers {
		info, err := fbs.GetFailedBlock(chain, blockNumber)
		if err != nil || info == nil {
			continue
		}
		if !info.Resolved {
			unresolvedBlocks = append(unresolvedBlocks, blockNumber)
		}
	}

	return unresolvedBlocks, nil
}

// GetAllFailedBlocks retrieves all failed block information for a chain (resolved and unresolved)
func (fbs *FailedBlockStore) GetAllFailedBlocks(chain string) ([]*FailedBlockInfo, error) {
	key := fbs.getFailedBlocksListKey(chain)

	data, err := fbs.kvstore.Get(key)
	if err != nil {
		// If no failed blocks list exists, return empty slice
		return []*FailedBlockInfo{}, nil
	}

	var blockNumbers []uint64
	if err := json.Unmarshal(data, &blockNumbers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal failed blocks list: %w", err)
	}

	var failedBlocks []*FailedBlockInfo
	for _, blockNumber := range blockNumbers {
		info, err := fbs.GetFailedBlock(chain, blockNumber)
		if err != nil || info == nil {
			continue
		}
		failedBlocks = append(failedBlocks, info)
	}

	return failedBlocks, nil
}

// ResolveFailedBlock marks a failed block as resolved
func (fbs *FailedBlockStore) ResolveFailedBlock(chain string, blockNumber uint64) error {
	info, err := fbs.GetFailedBlock(chain, blockNumber)
	if err != nil {
		return fmt.Errorf("failed to get failed block info: %w", err)
	}
	if info == nil {
		return fmt.Errorf("failed block not found: chain=%s, block=%d", chain, blockNumber)
	}

	info.Resolved = true
	info.Timestamp = time.Now()

	key := fbs.getBlockKey(chain, blockNumber)
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal failed block info: %w", err)
	}

	return fbs.kvstore.Set(key, data)
}

// DeleteFailedBlock removes a failed block from storage
func (fbs *FailedBlockStore) DeleteFailedBlock(chain string, blockNumber uint64) error {
	key := fbs.getBlockKey(chain, blockNumber)

	// Remove from individual block storage
	if err := fbs.kvstore.Delete(key); err != nil {
		return fmt.Errorf("failed to delete failed block: %w", err)
	}

	// Remove from failed blocks list
	return fbs.removeFromFailedBlocksList(chain, blockNumber)
}

// CleanupResolvedBlocks removes resolved blocks older than the specified duration
func (fbs *FailedBlockStore) CleanupResolvedBlocks(chain string, olderThan time.Duration) error {
	allBlocks, err := fbs.GetAllFailedBlocks(chain)
	if err != nil {
		return fmt.Errorf("failed to get all failed blocks: %w", err)
	}

	cutoffTime := time.Now().Add(-olderThan)

	for _, block := range allBlocks {
		if block.Resolved && block.Timestamp.Before(cutoffTime) {
			if err := fbs.DeleteFailedBlock(chain, block.BlockNumber); err != nil {
				// Log error but continue cleanup
				continue
			}
		}
	}

	return nil
}

// addToFailedBlocksList adds a block number to the chain's failed blocks list
func (fbs *FailedBlockStore) addToFailedBlocksList(chain string, blockNumber uint64) error {
	key := fbs.getFailedBlocksListKey(chain)

	// Get existing list
	data, err := fbs.kvstore.Get(key)
	var blockNumbers []uint64

	if err == nil {
		if err := json.Unmarshal(data, &blockNumbers); err != nil {
			return fmt.Errorf("failed to unmarshal existing failed blocks list: %w", err)
		}
	}

	// Check if block number already exists
	for _, existing := range blockNumbers {
		if existing == blockNumber {
			return nil // Already exists
		}
	}

	// Add new block number
	blockNumbers = append(blockNumbers, blockNumber)

	// Save updated list
	data, err = json.Marshal(blockNumbers)
	if err != nil {
		return fmt.Errorf("failed to marshal failed blocks list: %w", err)
	}

	return fbs.kvstore.Set(key, data)
}

// removeFromFailedBlocksList removes a block number from the chain's failed blocks list
func (fbs *FailedBlockStore) removeFromFailedBlocksList(chain string, blockNumber uint64) error {
	key := fbs.getFailedBlocksListKey(chain)

	// Get existing list
	data, err := fbs.kvstore.Get(key)
	if err != nil {
		return nil // List doesn't exist, nothing to remove
	}

	var blockNumbers []uint64
	if err := json.Unmarshal(data, &blockNumbers); err != nil {
		return fmt.Errorf("failed to unmarshal failed blocks list: %w", err)
	}

	// Remove the block number
	var updatedNumbers []uint64
	for _, existing := range blockNumbers {
		if existing != blockNumber {
			updatedNumbers = append(updatedNumbers, existing)
		}
	}

	// Save updated list
	data, err = json.Marshal(updatedNumbers)
	if err != nil {
		return fmt.Errorf("failed to marshal failed blocks list: %w", err)
	}

	return fbs.kvstore.Set(key, data)
}

// getBlockKey generates a unique key for a specific failed block
func (fbs *FailedBlockStore) getBlockKey(chain string, blockNumber uint64) string {
	return fmt.Sprintf("failed_block:%s:%d", chain, blockNumber)
}

// getFailedBlocksListKey generates a key for the chain's failed blocks list
func (fbs *FailedBlockStore) getFailedBlocksListKey(chain string) string {
	return fmt.Sprintf("failed_blocks_list:%s", chain)
}
