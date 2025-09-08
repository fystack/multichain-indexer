package blockstore

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/infra"
)

type Store struct {
	store infra.KVStore
}

func NewBlockStore(store infra.KVStore) *Store {
	return &Store{store: store}
}

func (bs *Store) GetLatestBlock(chainName string) (uint64, error) {
	startBlock, err := bs.store.Get(fmt.Sprintf("%s%s", constant.LatestBlockKeyPrefix, chainName))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(startBlock, 10, 64)
}

func (bs *Store) SaveLatestBlock(chainName string, blockNumber uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if blockNumber == 0 {
		return errors.New("block number is required")
	}
	logger.Info("Saving latest block", "chainName", chainName, "blockNumber", blockNumber)
	return bs.store.Set(fmt.Sprintf("%s%s", constant.LatestBlockKeyPrefix, chainName), strconv.FormatUint(blockNumber, 10))
}

func (bs *Store) GetFailedBlocks(chainName string) ([]uint64, error) {
	failedBlocks := []uint64{}
	ok, err := bs.store.GetAny(fmt.Sprintf("%s/%s", constant.FailedBlockKeyPrefix, chainName), &failedBlocks)
	if err != nil {
		return nil, err
	}
	if !ok {
		logger.Info("No failed blocks found", "chainName", chainName)
		return nil, nil
	}
	return failedBlocks, nil
}

// SaveFailedBlock appends a failed block to the per-chain list (deduplicated and sorted)
func (bs *Store) SaveFailedBlock(chainName string, blockNumber uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if blockNumber == 0 {
		return errors.New("block number is required")
	}

	key := fmt.Sprintf("%s/%s", constant.FailedBlockKeyPrefix, chainName)
	var blocks []uint64
	_, _ = bs.store.GetAny(key, &blocks) // ignore not found

	// dedup
	found := slices.Contains(blocks, blockNumber)
	if !found {
		blocks = append(blocks, blockNumber)
		slices.Sort(blocks)
		if err := bs.store.SetAny(key, blocks); err != nil {
			return err
		}
	}
	return nil
}

// RemoveFailedBlocksInRange removes all failed blocks within [start, end]
func (bs *Store) RemoveFailedBlocksInRange(chainName string, start, end uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if end < start {
		return nil
	}
	key := fmt.Sprintf("%s/%s", constant.FailedBlockKeyPrefix, chainName)
	var blocks []uint64
	ok, err := bs.store.GetAny(key, &blocks)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	filtered := make([]uint64, 0, len(blocks))
	for _, b := range blocks {
		if b < start || b > end {
			filtered = append(filtered, b)
		}
	}
	return bs.store.SetAny(key, filtered)
}

func (bs *Store) Close() error {
	return bs.store.Close()
}
