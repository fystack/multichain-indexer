package indexer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
)
const solAssetAddress = "SOL"

type SolanaIndexer struct {
	chainName string
	config    config.ChainConfig
	failover  *rpc.Failover[solana.SolanaAPI]
}

func NewSolanaIndexer(chainName string, cfg config.ChainConfig, failover *rpc.Failover[solana.SolanaAPI]) *SolanaIndexer {
	return &SolanaIndexer{chainName: chainName, config: cfg, failover: failover}
}

func (s *SolanaIndexer) GetName() string                  { return strings.ToUpper(s.chainName) }
func (s *SolanaIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeSol }
func (s *SolanaIndexer) GetNetworkInternalCode() string   { return s.config.InternalCode }

func (s *SolanaIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var slot uint64
	err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
		n, err := c.GetSlot(ctx)
		slot = n
		return err
	})
	return slot, err
}

func (s *SolanaIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	results, err := s.GetBlocksByNumbers(ctx, []uint64{number})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 || results[0].Error != nil {
		if len(results) > 0 && results[0].Error != nil {
			return nil, fmt.Errorf(results[0].Error.Message)
		}
		return nil, fmt.Errorf("block not found")
	}
	return results[0].Block, nil
}

func (s *SolanaIndexer) GetBlocks(ctx context.Context, from, to uint64, isParallel bool) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range")
	}
	nums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		nums = append(nums, n)
	}
	return s.GetBlocksByNumbers(ctx, nums)
}

func (s *SolanaIndexer) GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	results := make([]BlockResult, 0, len(blockNumbers))
	for _, slot := range blockNumbers {
		slot := slot
		var b *solana.GetBlockResult
		err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
			blk, err := c.GetBlock(ctx, slot)
			b = blk
			return err
		})
		if err != nil {
			results = append(results, BlockResult{Number: slot, Error: &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()}})
			continue
		}
		if b == nil {
			results = append(results, BlockResult{Number: slot, Error: &Error{ErrorType: ErrorTypeBlockNotFound, Message: "block not found (skipped slot?)"}})
			continue
		}

		timestamp := uint64(0)
		if b.BlockTime != nil {
			timestamp = uint64(*b.BlockTime)
		} else {
			timestamp = uint64(time.Now().UTC().Unix())
		}
		logger.Debug("[SOLANA] fetched block",
			"chain", s.chainName,
			"slot", slot,
			"txs", len(b.Transactions),
			"blockhash", b.Blockhash,
			"parent", b.PreviousBlockhash,
		)

		txs := extractSolanaTransfers(s.config.NetworkId, slot, timestamp, b)
		block := &types.Block{
			Number:       slot,
			Hash:         b.Blockhash,
			ParentHash:   b.PreviousBlockhash,
			Timestamp:    timestamp,
			Transactions: txs,
		}
		results = append(results, BlockResult{Number: slot, Block: block})
	}
	return results, nil
}

func (s *SolanaIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.GetLatestBlockNumber(ctx)
	return err == nil
}

func extractSolanaTransfers(networkID string, slot uint64, ts uint64, b *solana.GetBlockResult) []types.Transaction {
	out := make([]types.Transaction, 0)

	for _, tx := range b.Transactions {
		if tx.Meta == nil {
			continue
		}
		if tx.Meta.Err != nil {
			continue
		}
		if len(tx.Transaction.Signatures) == 0 {
			continue
		}
		txHash := tx.Transaction.Signatures[0]

		fee := decimal.NewFromInt(int64(tx.Meta.Fee))

		accountKeys := tx.Transaction.Message.AccountKeys
		preBalances := tx.Meta.PreBalances
		postBalances := tx.Meta.PostBalances

		// Native SOL transfers via balance deltas (lamports)
		if len(accountKeys) == len(preBalances) && len(accountKeys) == len(postBalances) {
			var solSenders []struct {
				addr  string
				delta uint64
			}
			var solReceivers []struct {
				addr  string
				delta uint64
			}

			for i, k := range accountKeys {
				preAmt := preBalances[i]
				postAmt := postBalances[i]
				if preAmt == postAmt {
					continue
				}
				if preAmt > postAmt {
					solSenders = append(solSenders, struct {
						addr  string
						delta uint64
					}{addr: k.Pubkey, delta: preAmt - postAmt})
				} else {
					solReceivers = append(solReceivers, struct {
						addr  string
						delta uint64
					}{addr: k.Pubkey, delta: postAmt - preAmt})
				}
			}

			// Pair senders -> receivers (best-effort). This is accurate for simple transfers.
			si, ri := 0, 0
			for si < len(solSenders) && ri < len(solReceivers) {
				s := &solSenders[si]
				r := &solReceivers[ri]

				amt := s.delta
				if r.delta < amt {
					amt = r.delta
				}
				if amt == 0 {
					if s.delta == 0 {
						si++
					}
					if r.delta == 0 {
						ri++
					}
					continue
				}

				out = append(out, types.Transaction{
					TxHash:        txHash,
					NetworkId:     networkID,
					BlockNumber:   slot,
					FromAddress:   s.addr,
					ToAddress:     r.addr,
					AssetAddress:  solAssetAddress,
					Amount:        fmt.Sprintf("%d", amt),
					Type:          "sol_transfer",
					TxFee:         fee,
					Timestamp:     ts,
					Confirmations: 1,
					Status:        types.CalculateStatus(1),
				})

				s.delta -= amt
				r.delta -= amt
				if s.delta == 0 {
					si++
				}
				if r.delta == 0 {
					ri++
				}
			}
		}

		// SPL token deltas via token balances (pre/post)
		preTok := map[string]map[string]uint64{}  // owner -> mint -> amount
		postTok := map[string]map[string]uint64{} // owner -> mint -> amount

		for _, tb := range tx.Meta.PreTokenBalances {
			if tb.Owner == "" || tb.Mint == "" {
				continue
			}
			amt, err := strconv.ParseUint(tb.UiTokenAmount.Amount, 10, 64)
			if err != nil {
				continue
			}
			if preTok[tb.Owner] == nil {
				preTok[tb.Owner] = map[string]uint64{}
			}
			preTok[tb.Owner][tb.Mint] = amt
		}
		for _, tb := range tx.Meta.PostTokenBalances {
			if tb.Owner == "" || tb.Mint == "" {
				continue
			}
			amt, err := strconv.ParseUint(tb.UiTokenAmount.Amount, 10, 64)
			if err != nil {
				continue
			}
			if postTok[tb.Owner] == nil {
				postTok[tb.Owner] = map[string]uint64{}
			}
			postTok[tb.Owner][tb.Mint] = amt
		}

		// Build token deltas per owner/mint
		type deltaItem struct {
			addr  string
			mint  string
			delta uint64
		}
		var tokenSenders []deltaItem
		var tokenReceivers []deltaItem

		seenOwners := map[string]bool{}
		for o := range preTok {
			seenOwners[o] = true
		}
		for o := range postTok {
			seenOwners[o] = true
		}

		for owner := range seenOwners {
			preM := preTok[owner]
			postM := postTok[owner]

			seenMints := map[string]bool{}
			for m := range preM {
				seenMints[m] = true
			}
			for m := range postM {
				seenMints[m] = true
			}

			for mint := range seenMints {
				preAmt := uint64(0)
				postAmt := uint64(0)
				if preM != nil {
					preAmt = preM[mint]
				}
				if postM != nil {
					postAmt = postM[mint]
				}
				if preAmt == postAmt {
					continue
				}
				if preAmt > postAmt {
					tokenSenders = append(tokenSenders, deltaItem{addr: owner, mint: mint, delta: preAmt - postAmt})
				} else {
					tokenReceivers = append(tokenReceivers, deltaItem{addr: owner, mint: mint, delta: postAmt - preAmt})
				}
			}
		}

		// Pair token senders -> receivers per mint (best-effort)
		for _, sender := range tokenSenders {
			remaining := sender.delta
			for i := 0; i < len(tokenReceivers) && remaining > 0; i++ {
				r := &tokenReceivers[i]
				if r.mint != sender.mint || r.delta == 0 {
					continue
				}
				amt := remaining
				if r.delta < amt {
					amt = r.delta
				}
				if amt == 0 {
					continue
				}

				out = append(out, types.Transaction{
					TxHash:        txHash,
					NetworkId:     networkID,
					BlockNumber:   slot,
					FromAddress:   sender.addr,
					ToAddress:     r.addr,
					AssetAddress:  sender.mint,
					Amount:        fmt.Sprintf("%d", amt),
					Type:          "token_transfer",
					TxFee:         fee,
					Timestamp:     ts,
					Confirmations: 1,
					Status:        types.CalculateStatus(1),
				})

				remaining -= amt
				r.delta -= amt
			}

			// If we couldn't pair, still emit a sender-only record so it isn't lost
			if remaining > 0 {
				out = append(out, types.Transaction{
					TxHash:        txHash,
					NetworkId:     networkID,
					BlockNumber:   slot,
					FromAddress:   sender.addr,
					ToAddress:     "",
					AssetAddress:  sender.mint,
					Amount:        fmt.Sprintf("%d", remaining),
					Type:          "token_transfer",
					TxFee:         fee,
					Timestamp:     ts,
					Confirmations: 1,
					Status:        types.CalculateStatus(1),
				})
			}
		}

		// Emit unpaired receivers (so deposits aren't missed)
		for _, r := range tokenReceivers {
			if r.delta == 0 {
				continue
			}
			out = append(out, types.Transaction{
				TxHash:        txHash,
				NetworkId:     networkID,
				BlockNumber:   slot,
				FromAddress:   "",
				ToAddress:     r.addr,
				AssetAddress:  r.mint,
				Amount:        fmt.Sprintf("%d", r.delta),
				Type:          "token_receive",
				TxFee:         fee,
				Timestamp:     ts,
				Confirmations: 1,
				Status:        types.CalculateStatus(1),
			})
		}
	}

	return out
}

