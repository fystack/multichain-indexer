// Package catchupstore persists catchup progress as a compact per-chain Redis
// hash instead of one KV key per range. Each chain keeps a single hash
//
//	catchup_progress:<chain>  { "<start>-<end>": "<current>" }
//
// so loading progress is a single HGETALL rather than a prefix scan over tens of
// thousands of keys (see issue #88). Unlike missingblockstore it has no
// claim/lock semantics — catchup scheduling is unchanged, only persistence.
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

// addRangesScript atomically inserts a range, merging it with any existing
// overlapping or adjacent ranges into a single field. Returns the merged bounds.
const addRangesScript = `
	local key = KEYS[1]
	local newStart = tonumber(ARGV[1])
	local newEnd = tonumber(ARGV[2])
	local newCurrent = tonumber(ARGV[3])

	if not newStart or not newEnd or newStart <= 0 or newEnd < newStart then
		return redis.error_reply("invalid catchup range")
	end

	if newCurrent > newEnd then
		newCurrent = newEnd
	end
	if newCurrent + 1 < newStart then
		newCurrent = newStart - 1
	end

	local mergedStart = newStart
	local mergedEnd = newEnd
	local mergedCurrent = newCurrent
	local entries = redis.call('HGETALL', key)

	for i = 1, #entries, 2 do
		local field = entries[i]
		local value = entries[i + 1]
		local existingStart, existingEnd = string.match(field, '^(%d+)%-(%d+)$')
		if existingStart and existingEnd then
			existingStart = tonumber(existingStart)
			existingEnd = tonumber(existingEnd)
			local existingCurrent = tonumber(value)

			if existingCurrent then
				if existingCurrent > existingEnd then
					existingCurrent = existingEnd
				end
				if existingCurrent + 1 < existingStart then
					existingCurrent = existingStart - 1
				end

				if existingStart <= mergedEnd + 1 and existingEnd + 1 >= mergedStart then
					if existingStart < mergedStart then
						mergedStart = existingStart
					end
					if existingEnd > mergedEnd then
						mergedEnd = existingEnd
					end
					if existingCurrent < mergedCurrent then
						mergedCurrent = existingCurrent
					end
					redis.call('HDEL', key, field)
				end
			end
		end
	end

	if mergedCurrent > mergedEnd then
		mergedCurrent = mergedEnd
	end
	if mergedCurrent + 1 < mergedStart then
		mergedCurrent = mergedStart - 1
	end

	local mergedField = tostring(mergedStart) .. '-' .. tostring(mergedEnd)
	redis.call('HSET', key, mergedField, tostring(mergedCurrent))
	return {mergedStart, mergedEnd, mergedCurrent}
`

// Store persists catchup ranges and their progress for a chain.
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
	// legacy is the previous one-key-per-range store, read once per chain to
	// migrate existing progress into the hash. May be nil.
	legacy blockstore.Store
}

// New returns a Redis-backed store, or a no-op store when Redis is unavailable.
// legacy is the previous KV-backed blockstore used for one-time migration.
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

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	key := composeKey(chain)
	for _, r := range ranges {
		if r.Start == 0 || r.End < r.Start {
			continue
		}
		if _, err := s.addScript.Run(
			ctx,
			s.redisClient.GetClient(),
			[]string{key},
			r.Start, r.End, r.Current,
		).Result(); err != nil {
			return fmt.Errorf("save catchup ranges: %w", err)
		}
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

// migrateFromLegacy copies one-key-per-range progress from the legacy KV store
// into the hash exactly once per chain. A marker key suppresses further legacy
// reads even after the hash later empties out.
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

	// Mark migrated regardless of whether legacy had data, so an empty legacy
	// store doesn't cause a re-read on every GetProgress.
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
