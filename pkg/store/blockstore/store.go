package blockstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/infra"
)

// BlockHashEntry represents a block number and its hash for reorg detection.
type BlockHashEntry struct {
	BlockNumber uint64 `json:"block_number"`
	Hash        string `json:"hash"`
}

const (
	BlockStates = "block_states"
)

type CatchupRange struct {
	Start   uint64 `json:"start"`
	End     uint64 `json:"end"`
	Current uint64 `json:"current"`
}

func latestBlockKey(chainName string) string {
	return fmt.Sprintf("%s/%s/%s", BlockStates, chainName, constant.KVPrefixLatestBlock)
}

func failedBlocksKey(chainName string) string {
	return fmt.Sprintf("%s/%s/%s", BlockStates, chainName, constant.KVPrefixFailedBlocks)
}

// Catchup progress keys
func composeCatchupKey(chain string) string {
	return fmt.Sprintf("%s/%s/%s/", BlockStates, chain, constant.KVPrefixProgressCatchup)
}

func blockHashesKey(chainName string) string {
	return fmt.Sprintf("%s/%s/%s", BlockStates, chainName, constant.KVPrefixBlockHash)
}

func catchupKey(chain string, start, end uint64) string {
	return fmt.Sprintf("%s/%s/%s/%d-%d", BlockStates, chain, constant.KVPrefixProgressCatchup, start, end)
}

type blockStore struct {
	store infra.KVStore
}

type Store interface {
	GetLatestBlock(chainName string) (uint64, error)
	SaveLatestBlock(chainName string, blockNumber uint64) error

	GetFailedBlocks(chainName string) ([]uint64, error)
	SaveFailedBlock(chainName string, blockNumber uint64) error
	SaveFailedBlocks(chainName string, blockNumbers []uint64) error
	RemoveFailedBlocks(chainName string, blockNumbers []uint64) error

	SaveCatchupProgress(chain string, start, end, current uint64) error
	GetCatchupProgress(chain string) ([]CatchupRange, error)
	DeleteCatchupRange(chain string, start, end uint64) error

	// Block hash persistence for reorg detection across restarts
	SaveBlockHashes(chainName string, hashes []BlockHashEntry) error
	GetBlockHashes(chainName string) ([]BlockHashEntry, error)

	Close() error
}

func NewBlockStore(store infra.KVStore) Store {
	return &blockStore{store: store}
}

func (bs *blockStore) GetLatestBlock(chainName string) (uint64, error) {
	startBlock, err := bs.store.Get(latestBlockKey(chainName))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(startBlock, 10, 64)
}

func (bs *blockStore) SaveLatestBlock(chainName string, blockNumber uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if blockNumber == 0 {
		return errors.New("block number is required")
	}
	return bs.store.Set(latestBlockKey(chainName), strconv.FormatUint(blockNumber, 10))
}

func (bs *blockStore) GetFailedBlocks(chainName string) ([]uint64, error) {
	failedBlocks := []uint64{}
	ok, err := bs.store.GetAny(failedBlocksKey(chainName), &failedBlocks)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return failedBlocks, nil
}

// SaveFailedBlock appends a failed block to the per-chain list (deduplicated and sorted)
func (bs *blockStore) SaveFailedBlock(chainName string, blockNumber uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if blockNumber == 0 {
		return errors.New("block number is required")
	}

	key := failedBlocksKey(chainName)
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

// SaveFailedBlocks appends multiple failed blocks (deduplicated + sorted).
func (bs *blockStore) SaveFailedBlocks(chainName string, blocksToAdd []uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if len(blocksToAdd) == 0 {
		return nil
	}

	key := failedBlocksKey(chainName)
	var blocks []uint64
	_, _ = bs.store.GetAny(key, &blocks) // ignore not found

	// merge + dedup
	for _, b := range blocksToAdd {
		if b == 0 {
			continue
		}
		if !slices.Contains(blocks, b) {
			blocks = append(blocks, b)
		}
	}
	slices.Sort(blocks)

	return bs.store.SetAny(key, blocks)
}

// RemoveFailedBlocks removes a set of blocks from the failed list.
func (bs *blockStore) RemoveFailedBlocks(chainName string, blockNumbers []uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if len(blockNumbers) == 0 {
		return nil
	}

	key := failedBlocksKey(chainName)
	var blocks []uint64
	ok, err := bs.store.GetAny(key, &blocks)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	toRemove := make(map[uint64]struct{}, len(blockNumbers))
	for _, b := range blockNumbers {
		toRemove[b] = struct{}{}
	}

	filtered := make([]uint64, 0, len(blocks))
	for _, b := range blocks {
		if _, drop := toRemove[b]; !drop {
			filtered = append(filtered, b)
		}
	}

	return bs.store.SetAny(key, filtered)
}

// SaveCatchupProgress saves or updates a catchup range with current progress.
func (bs *blockStore) SaveCatchupProgress(chain string, start, end, current uint64) error {
	if chain == "" || start == 0 || end < start {
		return errors.New("invalid catchup range")
	}
	key := catchupKey(chain, start, end)
	logger.Debug("Saving catchup progress to store",
		"chain", chain,
		"range", fmt.Sprintf("%d-%d", start, end),
		"current", current,
	)
	return bs.store.Set(key, fmt.Sprintf("%d", current))
}

// GetCatchupProgress returns all catchup ranges (struct-based).
func (bs *blockStore) GetCatchupProgress(chain string) ([]CatchupRange, error) {
	if chain == "" {
		return nil, errors.New("chain name is required")
	}
	prefix := composeCatchupKey(chain)
	kvs, err := bs.store.List(prefix)
	if err != nil {
		return nil, err
	}

	var ranges []CatchupRange
	for _, kv := range kvs {
		s, e := extractRangeFromKey(kv.Key)
		if s == 0 || e == 0 {
			continue
		}
		cur, _ := strconv.ParseUint(string(kv.Value), 10, 64)
		ranges = append(ranges, CatchupRange{Start: s, End: e, Current: cur})
		logger.Debug("Found catchup range in store",
			"chain", chain,
			"range", fmt.Sprintf("%d-%d", s, e),
			"current", cur,
		)
	}
	logger.Info("Loaded catchup progress from store",
		"chain", chain,
		"ranges_count", len(ranges),
	)
	return ranges, nil
}

// DeleteCatchupRange removes a saved range.
func (bs *blockStore) DeleteCatchupRange(chain string, start, end uint64) error {
	if chain == "" || start == 0 || end < start {
		return nil
	}
	key := catchupKey(chain, start, end)
	err := bs.store.Delete(key)
	if err != nil {
		logger.Error("Failed to delete catchup range from store",
			"chain", chain,
			"range", fmt.Sprintf("%d-%d", start, end),
			"key", key,
			"error", err,
		)
	}
	return err
}

// SaveBlockHashes persists block hashes for reorg detection across restarts.
func (bs *blockStore) SaveBlockHashes(chainName string, hashes []BlockHashEntry) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	data, err := json.Marshal(hashes)
	if err != nil {
		return fmt.Errorf("marshal block hashes: %w", err)
	}
	return bs.store.Set(blockHashesKey(chainName), string(data))
}

// GetBlockHashes loads persisted block hashes for reorg detection.
func (bs *blockStore) GetBlockHashes(chainName string) ([]BlockHashEntry, error) {
	if chainName == "" {
		return nil, errors.New("chain name is required")
	}
	raw, err := bs.store.Get(blockHashesKey(chainName))
	if err != nil {
		return nil, nil // Key not found is not an error
	}
	var hashes []BlockHashEntry
	if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
		return nil, fmt.Errorf("unmarshal block hashes: %w", err)
	}
	return hashes, nil
}

func (bs *blockStore) Close() error {
	return bs.store.Close()
}

func extractRangeFromKey(key string) (uint64, uint64) {
	// <chain>/catchup/<start>-<end>
	parts := strings.Split(key, "/")
	if len(parts) < 4 {
		return 0, 0
	}
	se := strings.Split(parts[len(parts)-1], "-")
	if len(se) != 2 {
		return 0, 0
	}
	s, err1 := strconv.ParseUint(se[0], 10, 64)
	e, err2 := strconv.ParseUint(se[1], 10, 64)
	if err1 == nil && err2 == nil && s <= e {
		return s, e
	}
	return 0, 0
}
