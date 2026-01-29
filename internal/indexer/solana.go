package indexer

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/mr-tron/base58"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

type SolanaIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[solana.SolanaAPI]
	pubkeyStore PubkeyStore
}

func NewSolanaIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[solana.SolanaAPI],
	pubkeyStore PubkeyStore,
) *SolanaIndexer {
	return &SolanaIndexer{chainName: chainName, config: cfg, failover: failover, pubkeyStore: pubkeyStore}
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
			return nil, fmt.Errorf("block error: %s", results[0].Error.Message)
		}
		return nil, fmt.Errorf("block not found")
	}

	if results[0].Block == nil {
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

	if !isParallel {
		return s.getBlocksByNumbersSequential(ctx, nums)
	}
	return s.GetBlocksByNumbers(ctx, nums)
}

func (s *SolanaIndexer) getBlocksByNumbersSequential(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
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

		txs := s.extractSolanaTransfers(s.config.NetworkId, slot, timestamp, b)
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

func (s *SolanaIndexer) GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return []BlockResult{}, nil
	}

	maxConc := s.config.Throttle.Concurrency
	if maxConc <= 0 {
		maxConc = 1
	}

	results := make([]BlockResult, len(blockNumbers))

	eg, egCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, maxConc)

	for i, slot := range blockNumbers {
		i := i
		slot := slot
		eg.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-egCtx.Done():
				return egCtx.Err()
			}

			var (
				b    *solana.GetBlockResult
				berr error
			)
			berr = s.failover.ExecuteWithRetry(egCtx, func(c solana.SolanaAPI) error {
				blk, err := c.GetBlock(egCtx, slot)
				b = blk
				return err
			})

			if berr != nil {
				results[i] = BlockResult{Number: slot, Error: &Error{ErrorType: ErrorTypeUnknown, Message: berr.Error()}}
				return nil
			}
			if b == nil {
				results[i] = BlockResult{Number: slot, Error: &Error{ErrorType: ErrorTypeBlockNotFound, Message: "block not found (skipped slot?)"}}
				return nil
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

			txs := s.extractSolanaTransfers(s.config.NetworkId, slot, timestamp, b)
			block := &types.Block{
				Number:       slot,
				Hash:         b.Blockhash,
				ParentHash:   b.PreviousBlockhash,
				Timestamp:    timestamp,
				Transactions: txs,
			}
			results[i] = BlockResult{Number: slot, Block: block}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return nil, err
		}
		return nil, err
	}

	return results, nil
}

func (s *SolanaIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.GetLatestBlockNumber(ctx)
	return err == nil
}

const solanaSystemProgramID = "11111111111111111111111111111111"
const solanaTokenProgramID = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
const solanaToken2022ProgramID = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"

func solanaInstructionAccountsToIndices(a any, accountKeys []solana.AccountKey) []int {
	switch v := a.(type) {
	case []any:
		out := make([]int, 0, len(v))
		for _, it := range v {
			switch n := it.(type) {
			case float64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			case uint64:
				out = append(out, int(n))
			}
		}
		return out
	case []uint64:
		out := make([]int, 0, len(v))
		for _, n := range v {
			out = append(out, int(n))
		}
		return out
	case []int:
		return v
	case []string:
		idxByPubkey := make(map[string]int, len(accountKeys))
		for i := range accountKeys {
			idxByPubkey[accountKeys[i].Pubkey] = i
		}
		out := make([]int, 0, len(v))
		for _, pk := range v {
			if i, ok := idxByPubkey[pk]; ok {
				out = append(out, i)
			} else {
				return nil
			}
		}
		return out
	default:
		return nil
	}
}

func solanaDecodeIxDataBase58(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base58.Decode(s)
}

func solanaParseSystemTransfer(ix solana.Instruction, accountKeys []solana.AccountKey) (from string, to string, lamports uint64, ok bool) {
	// Prefer jsonParsed format
	// Example:
	// { program:"system", programId:"111...", parsed:{ type:"transfer", info:{ source:"..", destination:"..", lamports:123 } } }
	if ix.ProgramId == solanaSystemProgramID || ix.Program == "system" {
		if p, _ := ix.Parsed.(map[string]any); p != nil {
			if t, _ := p["type"].(string); t == "transfer" {
				if info, _ := p["info"].(map[string]any); info != nil {
					src, _ := info["source"].(string)
					dst, _ := info["destination"].(string)
					var amt uint64
					switch v := info["lamports"].(type) {
					case float64:
						amt = uint64(v)
					case int:
						amt = uint64(v)
					case uint64:
						amt = v
					case string:
						if vv, err := decimal.NewFromString(v); err == nil {
							amt = vv.BigInt().Uint64()
						}
					}
					if src != "" && dst != "" && amt > 0 {
						return src, dst, amt, true
					}
				}
			}
		}
	}

	// Fallback for encoding=json where ix.Data is base58
	progIdx := int(ix.ProgramIdIndex)
	if progIdx < 0 || progIdx >= len(accountKeys) {
		return "", "", 0, false
	}
	if accountKeys[progIdx].Pubkey != solanaSystemProgramID {
		return "", "", 0, false
	}

	accIdx := solanaInstructionAccountsToIndices(ix.Accounts, accountKeys)
	if len(accIdx) < 2 {
		return "", "", 0, false
	}
	srcIdx := accIdx[0]
	dstIdx := accIdx[1]
	if srcIdx < 0 || srcIdx >= len(accountKeys) || dstIdx < 0 || dstIdx >= len(accountKeys) {
		return "", "", 0, false
	}

	data, err := solanaDecodeIxDataBase58(ix.Data)
	if err != nil {
		return "", "", 0, false
	}
	if len(data) != 12 {
		return "", "", 0, false
	}
	if binary.LittleEndian.Uint32(data[0:4]) != 2 {
		return "", "", 0, false
	}
	amt := binary.LittleEndian.Uint64(data[4:12])
	if amt == 0 {
		return "", "", 0, false
	}
	return accountKeys[srcIdx].Pubkey, accountKeys[dstIdx].Pubkey, amt, true
}

func solanaParseTokenTransfer(ix solana.Instruction, accountKeys []solana.AccountKey) (srcTokenAcc string, dstTokenAcc string, mint string, amount uint64, ok bool) {
	// Prefer jsonParsed format
	// Typical:
	// { program:"spl-token", programId:"Tokenkeg...", parsed:{ type:"transfer", info:{ source:"..", destination:"..", amount:"123", authority:".." } } }
	// TransferChecked:
	// { type:"transferChecked", info:{ source:"..", destination:"..", mint:"..", tokenAmount:{ amount:"..", decimals:n } } }
	if ix.Program == "spl-token" || ix.ProgramId == solanaTokenProgramID || ix.ProgramId == solanaToken2022ProgramID {
		if p, _ := ix.Parsed.(map[string]any); p != nil {
			t, _ := p["type"].(string)
			if info, _ := p["info"].(map[string]any); info != nil {
				var src, dst, mintOut string
				src, _ = info["source"].(string)
				dst, _ = info["destination"].(string)
				mintOut, _ = info["mint"].(string)

				var amt uint64
				switch t {
				case "transfer":
					// amount can be string or number
					switch v := info["amount"].(type) {
					case string:
						if vv, err := decimal.NewFromString(v); err == nil {
							amt = vv.BigInt().Uint64()
						}
					case float64:
						amt = uint64(v)
					case int:
						amt = uint64(v)
					case uint64:
						amt = v
					}
				case "transferChecked":
					// tokenAmount: { amount:"..", decimals:n, uiAmountString:".." }
					if ta, _ := info["tokenAmount"].(map[string]any); ta != nil {
						switch v := ta["amount"].(type) {
						case string:
							if vv, err := decimal.NewFromString(v); err == nil {
								amt = vv.BigInt().Uint64()
							}
						case float64:
							amt = uint64(v)
						case int:
							amt = uint64(v)
						case uint64:
							amt = v
						}
					}
				}

				if src != "" && dst != "" && amt > 0 {
					return src, dst, mintOut, amt, true
				}
			}
		}
	}

	// Fallback for encoding=json where ix.Data is base58
	progIdx := int(ix.ProgramIdIndex)
	if progIdx < 0 || progIdx >= len(accountKeys) {
		return "", "", "", 0, false
	}
	prog := accountKeys[progIdx].Pubkey
	if prog != solanaTokenProgramID && prog != solanaToken2022ProgramID {
		return "", "", "", 0, false
	}

	accIdx := solanaInstructionAccountsToIndices(ix.Accounts, accountKeys)
	data, err := solanaDecodeIxDataBase58(ix.Data)
	if err != nil {
		return "", "", "", 0, false
	}
	if len(data) < 1 {
		return "", "", "", 0, false
	}

	switch data[0] {
	case 3:
		if len(accIdx) < 2 || len(data) < 9 {
			return "", "", "", 0, false
		}
		srcIdx := accIdx[0]
		dstIdx := accIdx[1]
		if srcIdx < 0 || srcIdx >= len(accountKeys) || dstIdx < 0 || dstIdx >= len(accountKeys) {
			return "", "", "", 0, false
		}
		amt := binary.LittleEndian.Uint64(data[1:9])
		if amt == 0 {
			return "", "", "", 0, false
		}
		return accountKeys[srcIdx].Pubkey, accountKeys[dstIdx].Pubkey, "", amt, true
	case 12:
		if len(accIdx) < 3 || len(data) < 9 {
			return "", "", "", 0, false
		}
		srcIdx := accIdx[0]
		mintIdx := accIdx[1]
		dstIdx := accIdx[2]
		if srcIdx < 0 || srcIdx >= len(accountKeys) || dstIdx < 0 || dstIdx >= len(accountKeys) || mintIdx < 0 || mintIdx >= len(accountKeys) {
			return "", "", "", 0, false
		}
		amt := binary.LittleEndian.Uint64(data[1:9])
		if amt == 0 {
			return "", "", "", 0, false
		}
		return accountKeys[srcIdx].Pubkey, accountKeys[dstIdx].Pubkey, accountKeys[mintIdx].Pubkey, amt, true
	default:
		return "", "", "", 0, false
	}
}

func (s *SolanaIndexer) extractSolanaTransfers(networkID string, slot uint64, ts uint64, b *solana.GetBlockResult) []types.Transaction {
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

		// Build token-account -> (owner, mint) lookup from token balance metadata.
		// This isn't used to infer transfers; only to map SPL token accounts to owners/mints.
		tokenOwnerByAcc := map[string]string{}
		tokenMintByAcc := map[string]string{}
		for _, tb := range tx.Meta.PreTokenBalances {
			idx := int(tb.AccountIndex)
			if idx >= 0 && idx < len(accountKeys) {
				acc := accountKeys[idx].Pubkey
				if tb.Owner != "" {
					tokenOwnerByAcc[acc] = tb.Owner
				}
				if tb.Mint != "" {
					tokenMintByAcc[acc] = tb.Mint
				}
			}
		}
		for _, tb := range tx.Meta.PostTokenBalances {
			idx := int(tb.AccountIndex)
			if idx >= 0 && idx < len(accountKeys) {
				acc := accountKeys[idx].Pubkey
				if tb.Owner != "" {
					tokenOwnerByAcc[acc] = tb.Owner
				}
				if tb.Mint != "" {
					tokenMintByAcc[acc] = tb.Mint
				}
			}
		}

		appendNative := func(from, to string, lamports uint64) {
			if s.pubkeyStore != nil && !s.pubkeyStore.Exist(enum.NetworkTypeSol, to) {
				return
			}
			out = append(out, types.Transaction{
				TxHash:       txHash,
				NetworkId:    networkID,
				BlockNumber:  slot,
				FromAddress:  from,
				ToAddress:    to,
				AssetAddress: "",
				Amount:       strconv.FormatUint(lamports, 10),
				Type:         constant.TxTypeNativeTransfer,
				TxFee:        fee,
				Timestamp:    ts,
			})
		}

		appendSPL := func(srcTokenAcc, dstTokenAcc, mint string, amount uint64) {
			fromOwner := tokenOwnerByAcc[srcTokenAcc]
			toOwner := tokenOwnerByAcc[dstTokenAcc]

			if s.pubkeyStore != nil {
				if toOwner == "" || !s.pubkeyStore.Exist(enum.NetworkTypeSol, toOwner) {
					return
				}
			}

			if mint == "" {
				mint = tokenMintByAcc[srcTokenAcc]
				if mint == "" {
					mint = tokenMintByAcc[dstTokenAcc]
				}
			}
			if mint == "" {
				return
			}

			out = append(out, types.Transaction{
				TxHash:       txHash,
				NetworkId:    networkID,
				BlockNumber:  slot,
				FromAddress:  fromOwner,
				ToAddress:    toOwner,
				AssetAddress: mint,
				Amount:       strconv.FormatUint(amount, 10),
				Type:         constant.TxTypeTokenTransfer,
				TxFee:        fee,
				Timestamp:    ts,
			})
		}

		processIx := func(ix solana.Instruction) {
			if from, to, lamports, ok := solanaParseSystemTransfer(ix, accountKeys); ok {
				appendNative(from, to, lamports)
				return
			}
			if srcTok, dstTok, mint, amt, ok := solanaParseTokenTransfer(ix, accountKeys); ok {
				appendSPL(srcTok, dstTok, mint, amt)
				return
			}
		}

		for _, ix := range tx.Transaction.Message.Instructions {
			processIx(ix)
		}
		for _, ii := range tx.Meta.InnerInstructions {
			for _, ix := range ii.Instructions {
				processIx(ix)
			}
		}
	}

	return out
}
