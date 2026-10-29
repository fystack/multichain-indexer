package indexer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/adaptive"
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
	// shared across worker modes: one congestion controller per chain.
	limiter *adaptive.Limiter
}

func NewSolanaIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[solana.SolanaAPI],
	pubkeyStore PubkeyStore,
) *SolanaIndexer {
	maxConc := cfg.Throttle.Concurrency
	if maxConc <= 0 {
		maxConc = 1
	}
	limiter := adaptive.New(adaptive.Config{
		Max:            maxConc,
		Min:            1,
		LowLatency:     1200 * time.Millisecond,
		HighLatency:    2500 * time.Millisecond,
		AdjustInterval: time.Second,
		GrowStreak:     10,
	})
	return &SolanaIndexer{
		chainName:   chainName,
		config:      cfg,
		failover:    failover,
		pubkeyStore: pubkeyStore,
		limiter:     limiter,
	}
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
		b, err := s.fetchRawBlock(ctx, slot)
		results = append(results, s.toBlockResult(slot, b, err))
	}
	s.confirmSkippedSlots(ctx, results)
	return results, nil
}

func (s *SolanaIndexer) GetBlocksByNumbers(ctx context.Context, blockNumbers []uint64) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return []BlockResult{}, nil
	}

	results := make([]BlockResult, len(blockNumbers))
	eg, egCtx := errgroup.WithContext(ctx)
	for i, slot := range blockNumbers {
		eg.Go(func() error {
			if err := s.limiter.Acquire(egCtx); err != nil {
				return err
			}
			b, err := s.fetchRawBlock(egCtx, slot)
			// Parsing is CPU-only; free the RPC slot before it.
			s.limiter.Release()

			results[i] = s.toBlockResult(slot, b, err)
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	s.confirmSkippedSlots(ctx, results)
	return results, nil
}

func (s *SolanaIndexer) fetchRawBlock(ctx context.Context, slot uint64) (*solana.GetBlockResult, error) {
	var b *solana.GetBlockResult
	err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
		attemptStart := time.Now()
		blk, err := c.GetBlock(ctx, slot)
		// Per attempt, so retry back-off sleeps are not mistaken for congestion.
		s.limiter.Observe(time.Since(attemptStart), err == nil)
		b = blk
		return err
	})
	return b, err
}

func (s *SolanaIndexer) toBlockResult(slot uint64, b *solana.GetBlockResult, err error) BlockResult {
	if err != nil {
		return BlockResult{Number: slot, Error: &Error{ErrorType: ErrorTypeUnknown, Message: err.Error()}}
	}
	if b == nil {
		return BlockResult{Number: slot, Error: &Error{ErrorType: ErrorTypeBlockNotFound, Message: "block not found (skipped slot?)"}}
	}

	timestamp := uint64(time.Now().UTC().Unix())
	if b.BlockTime != nil {
		timestamp = uint64(*b.BlockTime)
	}
	logger.Debug("[SOLANA] fetched block",
		"chain", s.chainName,
		"slot", slot,
		"txs", len(b.Transactions),
		"blockhash", b.Blockhash,
		"parent", b.PreviousBlockhash,
	)

	return BlockResult{Number: slot, Block: &types.Block{
		Number:       slot,
		Hash:         b.Blockhash,
		ParentHash:   b.PreviousBlockhash,
		Timestamp:    timestamp,
		Transactions: s.extractSolanaTransfers(s.config.NetworkId, slot, timestamp, b),
	}}
}

func (s *SolanaIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.GetLatestBlockNumber(ctx)
	return err == nil
}

const (
	solanaSystemProgramID    = "11111111111111111111111111111111"
	solanaTokenProgramID     = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	solanaToken2022ProgramID = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

const (
	solanaSystemTransfer             = 2
	solanaSystemWithdrawNonceAccount = 5
	solanaSystemTransferWithSeed     = 11

	solanaTokenTransfer             = 3
	solanaTokenTransferChecked      = 12
	solanaTokenTransferFeeExtension = 26
	// Sub-instruction (data[1]) of the Token-2022 TransferFee extension.
	solanaTransferCheckedWithFee = 1
)

// System instructions that pay lamports to another account, mapped to the recipient's position
// in the instruction's account list (the payer is always position 0). CreateAccount and friends
// also move lamports but fund a brand-new account, not a wallet.
var solanaSystemTransferRecipient = map[uint32]int{
	solanaSystemTransfer:             1,
	solanaSystemWithdrawNonceAccount: 1,
	solanaSystemTransferWithSeed:     2,
}

var errSolanaNotTransfer = errors.New("not a transfer")

// solanaInstructionTransfer is one transfer as the instruction states it: wallets for native
// transfers, token accounts for SPL transfers. mint is set only when the instruction names it.
type solanaInstructionTransfer struct {
	native    bool
	token2022 bool
	from, to  string
	mint      string
	amount    uint64
	// fee is the withheld fee TransferCheckedWithFee states; feeKnown is false for every other instruction.
	fee      uint64
	feeKnown bool
}

// solanaParseInstruction returns errSolanaNotTransfer for anything that is not a native or token
// transfer, and another error for a System/Token instruction too malformed to read.
func solanaParseInstruction(ix solana.Instruction, keys []solana.AccountKey) (solanaInstructionTransfer, error) {
	if ix.ProgramIdIndex < 0 || ix.ProgramIdIndex >= len(keys) {
		return solanaInstructionTransfer{}, fmt.Errorf("program index %d out of range (%d keys)", ix.ProgramIdIndex, len(keys))
	}
	switch keys[ix.ProgramIdIndex].Pubkey {
	case solanaSystemProgramID:
		return solanaParseSystemTransfer(ix, keys)
	case solanaTokenProgramID:
		return solanaParseTokenTransfer(ix, keys)
	case solanaToken2022ProgramID:
		t, err := solanaParseTokenTransfer(ix, keys)
		t.token2022 = true
		return t, err
	default:
		return solanaInstructionTransfer{}, errSolanaNotTransfer
	}
}

func solanaParseSystemTransfer(ix solana.Instruction, keys []solana.AccountKey) (solanaInstructionTransfer, error) {
	data, err := base58.Decode(ix.Data)
	if err != nil {
		return solanaInstructionTransfer{}, fmt.Errorf("decode system data: %w", err)
	}
	if len(data) < 4 {
		return solanaInstructionTransfer{}, fmt.Errorf("system data too short: %d bytes", len(data))
	}
	recipient, isTransfer := solanaSystemTransferRecipient[binary.LittleEndian.Uint32(data[:4])]
	if !isTransfer {
		return solanaInstructionTransfer{}, errSolanaNotTransfer
	}
	if len(data) < 12 {
		return solanaInstructionTransfer{}, fmt.Errorf("system transfer data too short: %d bytes", len(data))
	}
	accounts, err := solanaResolveAccounts(ix.Accounts, keys, 0, recipient)
	if err != nil {
		return solanaInstructionTransfer{}, err
	}
	amount := binary.LittleEndian.Uint64(data[4:12])
	if amount == 0 {
		return solanaInstructionTransfer{}, errSolanaNotTransfer
	}
	return solanaInstructionTransfer{native: true, from: accounts[0], to: accounts[1], amount: amount}, nil
}

func solanaParseTokenTransfer(ix solana.Instruction, keys []solana.AccountKey) (solanaInstructionTransfer, error) {
	data, err := base58.Decode(ix.Data)
	if err != nil {
		return solanaInstructionTransfer{}, fmt.Errorf("decode token data: %w", err)
	}
	if len(data) == 0 {
		return solanaInstructionTransfer{}, errors.New("empty token instruction data")
	}

	var (
		minLen       int
		amountOffset = 1
		positions    []int // source, destination[, mint]
	)
	switch data[0] {
	case solanaTokenTransfer:
		minLen, positions = 9, []int{0, 1}
	case solanaTokenTransferChecked:
		minLen, positions = 10, []int{0, 2, 1}
	case solanaTokenTransferFeeExtension:
		if len(data) < 2 || data[1] != solanaTransferCheckedWithFee {
			return solanaInstructionTransfer{}, errSolanaNotTransfer
		}
		// [26, 1, amount u64, decimals u8, fee u64]
		minLen, amountOffset, positions = 19, 2, []int{0, 2, 1}
	default:
		return solanaInstructionTransfer{}, errSolanaNotTransfer
	}
	if len(data) < minLen {
		return solanaInstructionTransfer{}, fmt.Errorf("token instruction %d data too short: %d bytes", data[0], len(data))
	}
	accounts, err := solanaResolveAccounts(ix.Accounts, keys, positions...)
	if err != nil {
		return solanaInstructionTransfer{}, err
	}
	amount := binary.LittleEndian.Uint64(data[amountOffset : amountOffset+8])
	if amount == 0 {
		return solanaInstructionTransfer{}, errSolanaNotTransfer
	}
	t := solanaInstructionTransfer{from: accounts[0], to: accounts[1], amount: amount}
	if len(accounts) > 2 {
		t.mint = accounts[2]
	}
	if data[0] == solanaTokenTransferFeeExtension {
		t.fee, t.feeKnown = binary.LittleEndian.Uint64(data[11:19]), true
	}
	return t, nil
}

// solanaResolveAccounts returns the pubkeys at the given positions of an instruction's account list.
func solanaResolveAccounts(ixAccounts []int, keys []solana.AccountKey, positions ...int) ([]string, error) {
	out := make([]string, len(positions))
	for i, pos := range positions {
		if pos >= len(ixAccounts) {
			return nil, fmt.Errorf("instruction has %d accounts, need position %d", len(ixAccounts), pos)
		}
		idx := ixAccounts[pos]
		if idx < 0 || idx >= len(keys) {
			return nil, fmt.Errorf("account index %d out of range (%d keys)", idx, len(keys))
		}
		out[i] = keys[idx].Pubkey
	}
	return out, nil
}

// solanaEffectiveAccountKeys appends v0 ALT accounts (static + writable + readonly)
// so instruction/token-balance indices resolve under encoding=json.
func solanaEffectiveAccountKeys(static []solana.AccountKey, loaded *solana.LoadedAddresses) []solana.AccountKey {
	if loaded == nil || (len(loaded.Writable) == 0 && len(loaded.Readonly) == 0) {
		return static
	}
	out := make([]solana.AccountKey, 0, len(static)+len(loaded.Writable)+len(loaded.Readonly))
	out = append(out, static...)
	for _, pk := range loaded.Writable {
		out = append(out, solana.AccountKey{Pubkey: pk, Writable: true})
	}
	for _, pk := range loaded.Readonly {
		out = append(out, solana.AccountKey{Pubkey: pk})
	}
	return out
}

type solanaTokenAccount struct {
	owner, mint string
	// Spendable balance before/after the tx; excludes any withheld transfer fee.
	pre, post  uint64
	hasPost    bool
	badBalance bool
}

func solanaTokenAccounts(meta *solana.TxnMeta, keys []solana.AccountKey) map[string]solanaTokenAccount {
	out := make(map[string]solanaTokenAccount, len(meta.PostTokenBalances))
	for i, balances := range [][]solana.TokenBalance{meta.PreTokenBalances, meta.PostTokenBalances} {
		isPost := i == 1
		for _, tb := range balances {
			if tb.AccountIndex >= uint64(len(keys)) {
				continue
			}
			pubkey := keys[tb.AccountIndex].Pubkey
			acc := out[pubkey]
			if tb.Owner != "" {
				acc.owner = tb.Owner
			}
			if tb.Mint != "" {
				acc.mint = tb.Mint
			}
			amount, err := strconv.ParseUint(tb.UiTokenAmount.Amount, 10, 64)
			acc.badBalance = acc.badBalance || err != nil
			if isPost {
				acc.post, acc.hasPost = amount, true
			} else {
				acc.pre = amount
			}
			out[pubkey] = acc
		}
	}
	return out
}

// solanaTransfer is a transfer between wallets, before the watched-address filter.
type solanaTransfer struct {
	txIdx int
	// Position of the instruction: top-level "<i>", the j-th instruction it invokes "<i>.<j>".
	// Matches apex's legacy scanner so its (txHash, direction, transferIndex) dedup carries over.
	transferIndex string
	native        bool
	from, to      string
	asset         string
	// amount is what reached the recipient; sourceAmount, when non-zero, is the larger amount
	// the sender spent because a Token-2022 transfer fee was withheld.
	amount       uint64
	sourceAmount uint64

	token2022    bool
	dstTokenAcc  string
	fee          uint64
	feeKnown     bool
	feeUncertain bool
}

// solanaApplyTransferFees nets Token-2022 transfer fees out of amount. TransferCheckedWithFee states
// the fee. Plain TransferChecked on a fee mint does not, so the recipient's balance change is used
// instead, but only when no other transfer in the tx touches that account; otherwise the gross
// amount stays and the transfer is flagged.
func solanaApplyTransferFees(transfers []solanaTransfer, tokenAccounts map[string]solanaTokenAccount, touches map[string]int) {
	for i := range transfers {
		t := &transfers[i]
		if !t.token2022 {
			continue
		}
		net := t.amount
		switch dst := tokenAccounts[t.dstTokenAcc]; {
		case t.feeKnown:
			if t.fee <= t.amount {
				net = t.amount - t.fee
			}
		case touches[t.dstTokenAcc] != 1 || !dst.hasPost || dst.badBalance || dst.post <= dst.pre:
			t.feeUncertain = true
		case dst.post-dst.pre < t.amount:
			net = dst.post - dst.pre
		}
		if net != t.amount {
			t.sourceAmount, t.amount = t.amount, net
		}
	}
}

func (s *SolanaIndexer) extractSolanaTransfers(networkID string, slot uint64, ts uint64, b *solana.GetBlockResult) []types.Transaction {
	transfers := s.filterMonitored(s.parseSolanaTransfers(slot, b))
	out := make([]types.Transaction, 0, len(transfers))
	for _, t := range transfers {
		tx := b.Transactions[t.txIdx]
		txType := constant.TxTypeTokenTransfer
		if t.native {
			txType = constant.TxTypeNativeTransfer
		}
		transfer := types.Transaction{
			TxHash:        tx.Transaction.Signatures[0],
			NetworkId:     networkID,
			BlockNumber:   slot,
			BlockHash:     b.Blockhash,
			TransferIndex: t.transferIndex,
			FromAddress:   t.from,
			ToAddress:     t.to,
			AssetAddress:  t.asset,
			Amount:        strconv.FormatUint(t.amount, 10),
			Type:          txType,
			TxFee:         decimal.NewFromInt(int64(tx.Meta.Fee)),
			Timestamp:     ts,
		}
		if t.sourceAmount != 0 {
			transfer.SetMetadata(metadataKeySourceAmount, strconv.FormatUint(t.sourceAmount, 10))
		}
		if t.feeUncertain {
			logger.Warn("[SOLANA] Token-2022 amount may include a withheld transfer fee",
				"chain", s.chainName, "slot", slot, "tx", transfer.TxHash,
				"transfer_index", t.transferIndex, "mint", t.asset, "amount", transfer.Amount)
		}
		out = append(out, transfer)
	}
	return out
}

// NormalizeForDirection gives an outgoing event the amount the sender spent, fee included.
func (s *SolanaIndexer) NormalizeForDirection(tx types.Transaction, direction string) types.Transaction {
	return normalizeDirectionalMetadata(tx, direction)
}

func (s *SolanaIndexer) parseSolanaTransfers(slot uint64, b *solana.GetBlockResult) []solanaTransfer {
	var out []solanaTransfer
	for txIdx, tx := range b.Transactions {
		if tx.Meta == nil || tx.Meta.Err != nil || len(tx.Transaction.Signatures) == 0 {
			continue
		}
		keys := solanaEffectiveAccountKeys(tx.Transaction.Message.AccountKeys, tx.Meta.LoadedAddresses)
		instructions := tx.Transaction.Message.Instructions

		var innerByOuter [][]solana.Instruction
		if len(tx.Meta.InnerInstructions) > 0 {
			innerByOuter = make([][]solana.Instruction, len(instructions))
		}
		for _, group := range tx.Meta.InnerInstructions {
			if group.Index >= uint64(len(instructions)) {
				logger.Warn("[SOLANA] inner instructions point past the last instruction",
					"chain", s.chainName, "slot", slot, "tx", tx.Transaction.Signatures[0],
					"index", group.Index, "instructions", len(instructions))
				continue
			}
			innerByOuter[group.Index] = group.Instructions
		}

		txStart := len(out)
		var (
			tokenAccounts map[string]solanaTokenAccount
			touches       map[string]int
		)
		collect := func(ix solana.Instruction, transferIndex string) {
			parsed, err := solanaParseInstruction(ix, keys)
			if errors.Is(err, errSolanaNotTransfer) {
				return
			}
			if err != nil {
				// Skip rather than fail the block: a rescan would hit the same bytes again.
				logger.Warn("[SOLANA] skipping malformed instruction",
					"chain", s.chainName, "slot", slot, "tx", tx.Transaction.Signatures[0],
					"transfer_index", transferIndex, "error", err)
				return
			}

			t := solanaTransfer{txIdx: txIdx, transferIndex: transferIndex, native: parsed.native, amount: parsed.amount}
			if parsed.native {
				t.from, t.to = parsed.from, parsed.to
				out = append(out, t)
				return
			}

			if tokenAccounts == nil {
				tokenAccounts = solanaTokenAccounts(tx.Meta, keys)
				touches = make(map[string]int)
			}
			touches[parsed.from]++
			touches[parsed.to]++
			t.token2022, t.dstTokenAcc = parsed.token2022, parsed.to
			t.fee, t.feeKnown = parsed.fee, parsed.feeKnown

			src, dst := tokenAccounts[parsed.from], tokenAccounts[parsed.to]
			t.from, t.to = src.owner, dst.owner
			t.asset = parsed.mint
			if t.asset == "" {
				t.asset = src.mint
			}
			if t.asset == "" {
				t.asset = dst.mint
			}
			if t.asset == "" || (t.from == "" && t.to == "") {
				return
			}
			out = append(out, t)
		}

		for i, ix := range instructions {
			outer := strconv.Itoa(i)
			collect(ix, outer)
			if innerByOuter == nil {
				continue
			}
			for j, inner := range innerByOuter[i] {
				collect(inner, outer+"."+strconv.Itoa(j))
			}
		}
		if tokenAccounts != nil {
			solanaApplyTransferFees(out[txStart:], tokenAccounts, touches)
		}
	}
	return out
}

// filterMonitored keeps transfers touching a watched address, resolving every address in the
// block with one ExistBatch (BF.MEXISTS) call instead of a Redis round trip per transfer.
func (s *SolanaIndexer) filterMonitored(transfers []solanaTransfer) []solanaTransfer {
	if s.pubkeyStore == nil || len(transfers) == 0 {
		return transfers
	}

	monitored := make(map[string]bool)
	var addresses []string
	collect := func(addr string) {
		if addr == "" {
			return
		}
		if _, seen := monitored[addr]; seen {
			return
		}
		monitored[addr] = false
		addresses = append(addresses, addr)
	}
	for _, t := range transfers {
		collect(t.to)
		if s.config.TwoWayIndexing {
			collect(t.from)
		}
	}

	for i, exists := range s.pubkeyStore.ExistBatch(enum.NetworkTypeSol, addresses) {
		monitored[addresses[i]] = exists
	}

	out := transfers[:0]
	for _, t := range transfers {
		if monitored[t.to] || (s.config.TwoWayIndexing && monitored[t.from]) {
			out = append(out, t)
		}
	}
	return out
}

// Margin around candidate slots so a confirmed skip has produced blocks on both sides of it.
const solanaSkipConfirmMargin = 64

// confirmSkippedSlots keeps a "not found" slot as a skip only when getBlocks (possibly from another
// provider) lists blocks on both sides of it but not the slot itself. A node that pruned, jumped
// its ledger or lags returns null or -32007 for real blocks too; those become retryable failures.
func (s *SolanaIndexer) confirmSkippedSlots(ctx context.Context, results []BlockResult) {
	var candidates []int
	var lo, hi uint64
	for i, r := range results {
		if r.Error == nil || r.Error.ErrorType != ErrorTypeBlockNotFound {
			continue
		}
		if len(candidates) == 0 || r.Number < lo {
			lo = r.Number
		}
		if len(candidates) == 0 || r.Number > hi {
			hi = r.Number
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return
	}
	lo -= min(lo, solanaSkipConfirmMargin)
	hi += solanaSkipConfirmMargin

	var produced []uint64
	err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
		slots, err := c.GetBlocks(ctx, lo, hi)
		produced = slots
		return err
	})
	for _, i := range candidates {
		slot := results[i].Number
		if err == nil && solanaSlotConfirmedSkipped(produced, slot) {
			continue
		}
		msg := fmt.Sprintf("slot %d returned no block and is not a confirmed skip", slot)
		if err != nil {
			msg = fmt.Sprintf("%s: getBlocks failed: %v", msg, err)
		}
		results[i].Error = &Error{ErrorType: ErrorTypeUnknown, Message: msg}
	}
}

func solanaSlotConfirmedSkipped(produced []uint64, slot uint64) bool {
	idx, found := slices.BinarySearch(produced, slot)
	return !found && idx > 0 && idx < len(produced)
}
