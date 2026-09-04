// Package catchupstore persists catchup progress as one compact Redis hash per
// chain, catchup_progress:<chain> { "<start>-<end>": "<current>" }, so loading
// is a single HGETALL rather than a scan over tens of thousands of keys (#88).
package catchupstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/redis/go-redis/v9"
)

const (
	progressKeyPrefix = "catchup_progress"
	migratedKeyPrefix = "catchup_migrated"
	defaultTimeout    = 5 * time.Second
)

// addRangesScript merges the incoming ranges (flat (start,end,current) triplets
// in ARGV) with existing overlapping/adjacent ones in a single HGETALL + rewrite,
// keeping the whole save O(n) in one round-trip.
const addRangesScript = `
	local key = KEYS[1]

	local function clamp(r)
		if r.current > r.e then r.current = r.e end
		if r.current + 1 < r.s then r.current = r.s - 1 end
	end

	local ranges = {}

	local entries = redis.call('HGETALL', key)
	for i = 1, #entries, 2 do
		local existingStart, existingEnd = string.match(entries[i], '^(%d+)%-(%d+)$')
		local existingCurrent = tonumber(entries[i + 1])
		if existingStart and existingEnd and existingCurrent then
			local r = {s = tonumber(existingStart), e = tonumber(existingEnd), current = existingCurrent}
			clamp(r)
			ranges[#ranges + 1] = r
		end
	end

	for i = 1, #ARGV, 3 do
		local s = tonumber(ARGV[i])
		local e = tonumber(ARGV[i + 1])
		local current = tonumber(ARGV[i + 2])
		if not s or not e or s <= 0 or e < s then
			return redis.error_reply("invalid catchup range")
		end
		local r = {s = s, e = e, current = current}
		clamp(r)
		ranges[#ranges + 1] = r
	end

	table.sort(ranges, function(a, b)
		if a.s ~= b.s then return a.s < b.s end
		return a.e < b.e
	end)

	local merged = {}
	for _, r in ipairs(ranges) do
		local last = merged[#merged]
		if last and r.s <= last.e + 1 then
			if r.e > last.e then last.e = r.e end
			if r.current < last.current then last.current = r.current end
			clamp(last)
		else
			merged[#merged + 1] = r
		end
	end

	redis.call('DEL', key)
	for _, r in ipairs(merged) do
		redis.call('HSET', key, tostring(r.s) .. '-' .. tostring(r.e), tostring(r.current))
	end
	return #merged
`

type Store interface {
	SaveRanges(ctx context.Context, chain string, ranges []blockstore.CatchupRange) error
	SaveProgress(ctx context.Context, chain string, start, end, current uint64) error
	GetProgress(ctx context.Context, chain string) ([]blockstore.CatchupRange, error)
	DeleteRange(ctx context.Context, chain string, start, end uint64) error
}

type noopStore struct{}

func (noopStore) SaveRanges(context.Context, string, []blockstore.CatchupRange) error { return nil }
func (noopStore) SaveProgress(context.Context, string, uint64, uint64, uint64) error  { return nil }
func (noopStore) GetProgress(context.Context, string) ([]blockstore.CatchupRange, error) {
	return nil, nil
}
func (noopStore) DeleteRange(context.Context, string, uint64, uint64) error { return nil }

type catchupStore struct {
	redisClient infra.RedisClient
	addScript   *redis.Script
	// legacy is the previous one-key-per-range store, migrated in once per chain. May be nil.
	legacy blockstore.Store
}

// New returns a Redis-backed store, or a no-op store when Redis is unavailable.
func New(redisClient infra.RedisClient, legacy blockstore.Store) Store {
	if redisClient == nil || redisClient.GetClient() == nil {
		return noopStore{}
	}
	return &catchupStore{
		redisClient: redisClient,
		addScript:   redis.NewScript(addRangesScript),
		legacy:      legacy,
	}
}

func composeKey(chain string) string {
	return fmt.Sprintf("%s:%s", progressKeyPrefix, chain)
}

func composeMigratedKey(chain string) string {
	return fmt.Sprintf("%s:%s", migratedKeyPrefix, chain)
}

func composeField(start, end uint64) string {
	return fmt.Sprintf("%d-%d", start, end)
}

func parseField(field string) (uint64, uint64, bool) {
	parts := strings.Split(field, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || end < start || start == 0 {
		return 0, 0, false
	}
	return start, end, true
}

func (s *catchupStore) SaveRanges(
	ctx context.Context,
	chain string,
	ranges []blockstore.CatchupRange,
) error {
	if chain == "" {
		return errors.New("chain name is required")
	}
	if len(ranges) == 0 {
		return nil
	}

	args := make([]any, 0, len(ranges)*3)
	for _, r := range ranges {
		if r.Start == 0 || r.End < r.Start {
			continue
		}
		args = append(args, r.Start, r.End, r.Current)
	}
	if len(args) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	if _, err := s.addScript.Run(
		ctx,
		s.redisClient.GetClient(),
		[]string{composeKey(chain)},
		args...,
	).Result(); err != nil {
		return fmt.Errorf("save catchup ranges: %w", err)
	}
	return nil
}

func (s *catchupStore) SaveProgress(
	ctx context.Context,
	chain string,
	start, end, current uint64,
) error {
	if chain == "" || start == 0 || end < start {
		return errors.New("invalid catchup range")
	}
	if current > end {
		current = end
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	if err := s.redisClient.GetClient().
		HSet(ctx, composeKey(chain), composeField(start, end), strconv.FormatUint(current, 10)).
		Err(); err != nil {
		return fmt.Errorf("save catchup progress: %w", err)
	}
	return nil
}

func (s *catchupStore) GetProgress(
	ctx context.Context,
	chain string,
) ([]blockstore.CatchupRange, error) {
	if chain == "" {
		return nil, errors.New("chain name is required")
	}

	if err := s.migrateFromLegacy(ctx, chain); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	values, err := s.redisClient.GetClient().HGetAll(ctx, composeKey(chain)).Result()
	if err != nil {
		return nil, fmt.Errorf("get catchup progress: %w", err)
	}
	return parseProgressMap(values), nil
}

func (s *catchupStore) DeleteRange(
	ctx context.Context,
	chain string,
	start, end uint64,
) error {
	if chain == "" || start == 0 || end < start {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	key := composeKey(chain)
	pipe := s.redisClient.GetClient().Pipeline()
	pipe.HDel(ctx, key, composeField(start, end))
	lenCmd := pipe.HLen(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete catchup range: %w", err)
	}
	// Drop the empty hash so the chain leaves no dangling key.
	if lenCmd.Val() == 0 {
		if err := s.redisClient.GetClient().Del(ctx, key).Err(); err != nil {
			return fmt.Errorf("cleanup catchup key: %w", err)
		}
	}
	return nil
}

// migrateFromLegacy copies legacy KV progress into the hash once per chain,
// guarded by a marker key so it isn't re-read after the hash later empties out.
func (s *catchupStore) migrateFromLegacy(ctx context.Context, chain string) error {
	if s.legacy == nil {
		return nil
	}

	mctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	marker := composeMigratedKey(chain)
	exists, err := s.redisClient.GetClient().Exists(mctx, marker).Result()
	if err != nil {
		return fmt.Errorf("check catchup migration marker: %w", err)
	}
	if exists == 1 {
		return nil
	}

	legacyRanges, err := s.legacy.GetCatchupProgress(chain)
	if err == nil && len(legacyRanges) > 0 {
		if err := s.SaveRanges(ctx, chain, legacyRanges); err != nil {
			return fmt.Errorf("migrate catchup ranges: %w", err)
		}
	}

	// Mark migrated even when legacy was empty, to avoid a re-read on every GetProgress.
	if err := s.redisClient.GetClient().Set(mctx, marker, "1", 0).Err(); err != nil {
		return fmt.Errorf("set catchup migration marker: %w", err)
	}
	return nil
}

func parseProgressMap(values map[string]string) []blockstore.CatchupRange {
	ranges := make([]blockstore.CatchupRange, 0, len(values))
	for field, currentText := range values {
		start, end, ok := parseField(field)
		if !ok {
			continue
		}
		current, err := strconv.ParseUint(currentText, 10, 64)
		if err != nil {
			continue
		}
		if current > end {
			current = end
		}
		ranges = append(ranges, blockstore.CatchupRange{Start: start, End: end, Current: current})
	}

	slices.SortFunc(ranges, func(a, b blockstore.CatchupRange) int {
		switch {
		case a.Start < b.Start:
			return -1
		case a.Start > b.Start:
			return 1
		case a.End < b.End:
			return -1
		case a.End > b.End:
			return 1
		default:
			return 0
		}
	})
	return ranges
}
