package indexer

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/infra"
)

type BlockStore struct {
	store infra.KVStore
}

func NewBlockStore(store infra.KVStore) *BlockStore {
	return &BlockStore{store: store}
}

func (bs *BlockStore) GetLatestBlock(chainName string) (uint64, error) {
	startBlock, err := bs.store.Get(fmt.Sprintf("%s%s", constant.LatestBlockKeyPrefix, chainName))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(startBlock, 10, 64)
}

func (bs *BlockStore) SaveLatestBlock(chainName string, blockNumber uint64) error {
	if chainName == "" {
		return errors.New("chain name is required")
	}
	if blockNumber == 0 {
		return errors.New("block number is required")
	}
	logger.Info("Saving latest block", "chainName", chainName, "blockNumber", blockNumber)
	return bs.store.Set(fmt.Sprintf("%s%s", constant.LatestBlockKeyPrefix, chainName), strconv.FormatUint(blockNumber, 10))
}

func (bs *BlockStore) Close() error {
	return bs.store.Close()
}
