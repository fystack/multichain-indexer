package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/pkg/common/types"
)

// Account watching (solana.watch_mode: accounts): instead of reading every slot, the worker reads the balances of the
// watched accounts and fetches the transactions of those that changed. These are the calls it makes.

// AccountStates reads the watched accounts at the finalized commitment, MaxMultipleAccounts per call. It returns the
// slot they were read at (the lowest of the calls) and one fingerprint per key, in order: "" for an account that
// doesn't exist yet, otherwise its lamports, owner and data, so any balance change changes it.
func (s *SolanaIndexer) AccountStates(ctx context.Context, keys []string) (uint64, []string, error) {
	fingerprints := make([]string, 0, len(keys))
	var slot uint64
	for start := 0; start < len(keys); start += solana.MaxMultipleAccounts {
		end := min(start+solana.MaxMultipleAccounts, len(keys))
		batch := keys[start:end]

		var res *solana.GetMultipleAccountsResult
		err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
			r, err := c.GetMultipleAccounts(ctx, batch)
			res = r
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		if slot == 0 || res.Context.Slot < slot {
			slot = res.Context.Slot
		}
		for _, acc := range res.Value {
			if acc == nil {
				fingerprints = append(fingerprints, "")
				continue
			}
			fingerprints = append(fingerprints, fmt.Sprintf("%d/%s/%s", acc.Lamports, acc.Owner, strings.Join(acc.Data, "/")))
		}
	}
	return slot, fingerprints, nil
}

// NewSignatures returns address's finalized signatures after until (a signature already processed; "" for none) and
// above slot floor, oldest first. Without until it pages back until it passes floor.
func (s *SolanaIndexer) NewSignatures(ctx context.Context, address, until string, floor uint64) ([]solana.SignatureInfo, error) {
	var newestFirst []solana.SignatureInfo
	before := ""
	for {
		var page []solana.SignatureInfo
		err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
			p, err := c.GetSignaturesForAddress(ctx, address, solana.SignaturesOptions{
				Before: before,
				Until:  until,
				Limit:  solana.MaxSignaturesPerPage,
			})
			page = p
			return err
		})
		if err != nil {
			return nil, err
		}

		reachedFloor := false
		for _, sig := range page {
			if sig.Slot <= floor {
				reachedFloor = true
				break
			}
			newestFirst = append(newestFirst, sig)
		}
		if reachedFloor || len(page) < solana.MaxSignaturesPerPage {
			break
		}
		before = page[len(page)-1].Signature
	}

	oldestFirst := make([]solana.SignatureInfo, len(newestFirst))
	for i, sig := range newestFirst {
		oldestFirst[len(newestFirst)-1-i] = sig
	}
	return oldestFirst, nil
}

// TransactionBlock fetches one transaction and returns its monitored transfers as a one-transaction block at its slot.
// Transfer indexes use the transaction's position in its block when the node reports it, as reading the block would.
func (s *SolanaIndexer) TransactionBlock(ctx context.Context, signature string) (*types.Block, error) {
	var res *solana.GetTransactionResult
	err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
		r, err := c.GetTransaction(ctx, signature)
		res = r
		return err
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("transaction %s not found", signature)
	}

	timestamp := uint64(time.Now().UTC().Unix())
	if res.BlockTime != nil {
		timestamp = uint64(*res.BlockTime)
	}
	txIdx := 0
	if res.TransactionIndex != nil {
		txIdx = *res.TransactionIndex
	}
	tx := solana.BlockTxn{Meta: res.Meta, Transaction: res.Transaction}
	return &types.Block{
		Number:       res.Slot,
		Timestamp:    timestamp,
		Transactions: s.extractTxTransfers(s.config.NetworkId, res.Slot, timestamp, "", txIdx, tx),
	}, nil
}

// TokenAccounts lists owner's token accounts under both SPL token programs.
func (s *SolanaIndexer) TokenAccounts(ctx context.Context, owner string) ([]string, error) {
	var all []string
	for _, program := range SolanaTokenProgramIDs {
		var keys []string
		err := s.failover.ExecuteWithRetry(ctx, func(c solana.SolanaAPI) error {
			k, err := c.GetTokenAccountsByOwner(ctx, owner, program)
			keys = k
			return err
		})
		if err != nil {
			return nil, err
		}
		all = append(all, keys...)
	}
	return all, nil
}
