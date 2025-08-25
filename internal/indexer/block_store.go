package indexer

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/fystack/transaction-indexer/pkg/kvstore"
)

type BlockStore struct {
	store kvstore.KVStore
}

func NewBlockStore(store kvstore.KVStore) *BlockStore {
	return &BlockStore{store: store}
}

func (bs *BlockStore) GetLatestBlock(chainName string) (uint64, error) {
	startBlock, err := bs.store.Get(fmt.Sprintf("latest_block_%s", chainName))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(startBlock), 10, 64)
}

func (bs *BlockStore) SaveLatestBlock(chainName string, blockNumber uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if blockNumber == 0 {
		return errors.New("block number is required")
	}
	slog.Info("Saving latest block", "chainName", chainName, "blockNumber", blockNumber)
	return bs.store.Set(fmt.Sprintf("latest_block_%s", chainName), []byte(strconv.FormatUint(blockNumber, 10)))
}

func (bs *BlockStore) Close() error {
	return bs.store.Close()
}
