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

// keccak256("Transfer(address,address,uint256)")
const ERC_TRANSFER_TOPIC = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

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
		fee := decimal.NewFromInt(tx.Fee)
		assignedFee := false // only assign fee to the first entry of this tx

		// TRC20/721 logs
		if len(tx.Log) > 0 {
			transfers := t.parseTRC20Logs(tx)

			for i := range transfers {
				if !assignedFee {
					transfers[i].TxFee = fee
					assignedFee = true
				}
			}
			block.Transactions = append(block.Transactions, transfers...)
		}

		// Internal: TRX & TRC10
		for _, itx := range tx.InternalTransactions {
			from := rpc.TronToHexAddress(itx.CallerAddress)
			to := rpc.TronToHexAddress(itx.TransferToAddress)

			for _, v := range itx.CallValueInfo {
				amount := decimal.NewFromInt(v.CallValue)

				tr := core.Transaction{
					TxHash:      tx.ID,
					NetworkId:   t.chainName,
					BlockNumber: uint64(tx.BlockNumber),
					FromAddress: from,
					ToAddress:   to,
					Amount:      amount.String(),
					Timestamp:   uint64(tx.BlockTimestamp),
				}

				if strings.TrimSpace(v.TokenName) == "" {
					// Native TRX (internal)
					tr.Type = "transfer"
					tr.AssetAddress = ""
				} else {
					// TRC10 asset (InternalTransaction.TokenName)
					tr.Type = "trc10_transfer"
					tr.AssetAddress = v.TokenName
				}

				if !assignedFee {
					tr.TxFee = fee
					assignedFee = true
				}
				fmt.Println("tr", tr)
				block.Transactions = append(block.Transactions, tr)
			}
		}
	}

	return block, nil
}

// parseTRC20Logs converts Tron logs that represent ERC-20-compatible Transfer events into core.Transaction entries
func (t *TronIndexer) parseTRC20Logs(tx *rpc.TronTransactionInfo) []core.Transaction {
	var transfers []core.Transaction

	for _, log := range tx.Log {
		// should have at least 3 topics: 0=event, 1=from, 2=to
		if len(log.Topics) < 3 {
			continue
		}

		topic0 := strings.ToLower(strings.TrimSpace(log.Topics[0]))
		// Normalize to compare without 0x prefix differences
		topic0 = strings.TrimPrefix(topic0, "0x")
		if topic0 != strings.TrimPrefix(ERC_TRANSFER_TOPIC, "0x") {
			continue
		}

		from, ok1 := extractAddressFromTopic(log.Topics[1])
		to, ok2 := extractAddressFromTopic(log.Topics[2])
		if !ok1 || !ok2 {
			continue
		}

		amountBI, err := parseHexBigInt(log.Data)
		if err != nil {
			continue
		}

		// log.Address in Tron doesn't have 0x41 prefix; TronToHexAddress already handled it
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
			TxFee:        decimal.Zero,
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

// extractAddressFromTopic get last 20 bytes of topic (32 bytes) -> "0x" + 40 hex chars
func extractAddressFromTopic(topic string) (string, bool) {
	s := strings.TrimSpace(topic)
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if len(s) < 40 {
		return "", false
	}
	if len(s) >= 64 {
		return "0x" + s[len(s)-40:], true
	}
	// sometimes node returns already 20-byte (rare), fallback
	return "0x" + s, true
}
