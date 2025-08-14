package indexer

import (
	"context"
	"fmt"
	"idx/internal/common/ratelimiter"
	"idx/internal/core"
	"idx/internal/rpc"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type TronIndexer struct {
	chainName   string
	config      core.ChainConfig
	failover    *rpc.FailoverManager
	rateLimiter *ratelimiter.PooledRateLimiter
}

func NewTronIndexer(config core.ChainConfig) (*TronIndexer, error) {
	var rl *ratelimiter.PooledRateLimiter
	if config.Client.Throttle.RPS > 0 {
		interval := time.Second / time.Duration(config.Client.Throttle.RPS)
		burst := config.Client.Throttle.Burst
		if burst <= 0 {
			burst = 1
		}
		rl = ratelimiter.NewPooledRateLimiter(interval, burst)
	}

	fm := rpc.NewFailoverManager(rpc.DefaultFailoverConfig())
	for i, nodeURL := range config.Nodes {
		name := fmt.Sprintf("%s-%d", config.Name, i)
		if err := fm.AddTronProvider(name, nodeURL.URL, nil, rl); err != nil {
			return nil, fmt.Errorf("add provider: %w", err)
		}
	}

	return &TronIndexer{
		chainName:   config.Name,
		config:      config,
		failover:    fm,
		rateLimiter: rl,
	}, nil
}

func (t *TronIndexer) GetName() string { return t.chainName }

func (t *TronIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
		n, err := c.GetNowBlock(ctx)
		latest = n
		return err
	})
	return latest, err
}

func (t *TronIndexer) GetBlock(ctx context.Context, blockNumber uint64) (*core.Block, error) {
	// Fetch block metadata
	var tronBlock *rpc.TronBlock
	if err := t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
		b, err := c.GetBlockByNumber(ctx, fmt.Sprintf("%d", blockNumber), true)
		if err != nil {
			return err
		}
		tronBlock = b
		return nil
	}); err != nil {
		return nil, err
	}

	// Fetch transaction infos for the block (contains logs and fee info)
	var txns []*rpc.TronTransactionInfo
	if err := t.failover.ExecuteTronCall(ctx, func(c *rpc.TronClient) error {
		var err error
		txns, err = c.GetTransactionInfoByBlockNum(ctx, int64(blockNumber))
		return err
	}); err != nil {
		return nil, err
	}
	block := &core.Block{
		Number:       uint64(tronBlock.BlockHeader.RawData.Number),
		Hash:         tronBlock.BlockID,
		ParentHash:   tronBlock.BlockHeader.RawData.ParentHash,
		Timestamp:    uint64(tronBlock.BlockHeader.RawData.Timestamp),
		Transactions: []core.Transaction{},
	}

	for _, tx := range txns {
		// Native TRX fee as transaction fee
		fee := decimal.NewFromInt(tx.Fee)
		fmt.Println("tx", tx.ID, tx.Log)
		// TRC20 logs
		if len(tx.Log) > 0 {
			transfers := t.parseTRC20Logs(tx)
			for i := range transfers {
				// Prefer the actual per-tx fee on first entry; others zero to avoid overcounting
				if i == 0 {
					transfers[i].TxFee = fee
				}
			}
			block.Transactions = append(block.Transactions, transfers...)
		}

		// TRC10 and native TRX are not in logs; parse by inspecting internal transfers and receipt
		// Native TRX is reflected in internal transactions with tokenName empty
		for _, itx := range tx.InternalTransactions {
			from := rpc.TronToHexAddress(itx.CallerAddress)
			to := rpc.TronToHexAddress(itx.TransferToAddress)
			for _, v := range itx.CallValueInfo {
				amount := decimal.NewFromInt(v.CallValue)
				if strings.TrimSpace(v.TokenName) == "" {
					// Native TRX transfer
					block.Transactions = append(block.Transactions, core.Transaction{
						TxHash:       tx.ID,
						NetworkId:    t.chainName,
						BlockNumber:  uint64(tx.BlockNumber),
						FromAddress:  from,
						ToAddress:    to,
						AssetAddress: "",
						Amount:       amount.String(),
						Type:         "transfer",
						TxFee:        fee,
						Timestamp:    uint64(tx.BlockTimestamp),
					})
				} else {
					// TRC10 transfer (TokenName carries asset identifier)
					block.Transactions = append(block.Transactions, core.Transaction{
						TxHash:       tx.ID,
						NetworkId:    t.chainName,
						BlockNumber:  uint64(tx.BlockNumber),
						FromAddress:  from,
						ToAddress:    to,
						AssetAddress: v.TokenName,
						Amount:       amount.String(),
						Type:         "trc10_transfer",
						TxFee:        fee,
						Timestamp:    uint64(tx.BlockTimestamp),
					})
				}
			}
		}
	}
	return block, nil
}

// parseTRC20Logs converts Tron logs that represent ERC-20-compatible Transfer events into core.Transaction entries
func (t *TronIndexer) parseTRC20Logs(tx *rpc.TronTransactionInfo) []core.Transaction {
	var transfers []core.Transaction
	for _, log := range tx.Log {
		if len(log.Topics) < 3 {
			continue
		}
		// Topics are expected to be 0x-prefixed hex strings
		topic0 := strings.ToLower(strings.TrimSpace(log.Topics[0]))
		if topic0 != ERC20_TRANSFER_TOPIC {
			continue
		}
		fromTopic := strings.ToLower(strings.TrimSpace(log.Topics[1]))
		toTopic := strings.ToLower(strings.TrimSpace(log.Topics[2]))

		from := "0x" + fromTopic[len(fromTopic)-40:]
		to := "0x" + toTopic[len(toTopic)-40:]

		amountBI, err := parseHexBigInt(log.Data)
		if err != nil {
			continue
		}

		assetHex := rpc.TronToHexAddress(log.Address)
		transfers = append(transfers, core.Transaction{
			TxHash:       tx.ID,
			NetworkId:    t.chainName,
			BlockNumber:  uint64(tx.BlockNumber),
			FromAddress:  from,
			ToAddress:    to,
			AssetAddress: assetHex,
			Amount:       amountBI.String(),
			Type:         "erc20_transfer",
			TxFee:        decimal.NewFromInt(tx.Fee),
			Timestamp:    uint64(tx.BlockTimestamp),
		})
	}
	return transfers
}

func (t *TronIndexer) GetBlocks(ctx context.Context, start, end uint64) ([]BlockResult, error) {
	if start > end {
		return nil, fmt.Errorf("start block number is greater than end block number")
	}

	blocks := make([]BlockResult, 0)
	for i := start; i <= end; i++ {
		block, err := t.GetBlock(ctx, i)
		if err != nil {
			return nil, fmt.Errorf("get block: %w", err)
		}
		blocks = append(blocks, BlockResult{
			Number: i,
			Block:  block,
			Error:  nil,
		})
	}
	return blocks, nil
}

func (t *TronIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := t.GetLatestBlockNumber(ctx)
	return err == nil
}
