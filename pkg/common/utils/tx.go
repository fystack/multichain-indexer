package utils

import (
	"fmt"

	"github.com/fystack/transaction-indexer/pkg/common/types"
)

func DedupTransfers(in []types.Transaction) []types.Transaction {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	var out []types.Transaction
	for _, t := range in {
		key := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d",
			t.TxHash, t.Type, t.AssetAddress, t.FromAddress, t.ToAddress, t.Amount, t.BlockNumber)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

func FilterMap[K comparable, V any](all map[K]V, keys []K, nonNilOnly bool) map[K]V {
	out := make(map[K]V, len(keys))
	for _, k := range keys {
		if v, ok := all[k]; ok {
			if nonNilOnly {
				// only add if non-nil pointer
				if vv, ok := any(v).(*struct{}); ok && vv == nil {
					continue
				}
			}
			out[k] = v
		}
	}
	return out
}
