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
	catchupProgressKeyPrefix = "catchup_progress"
	catchupLockKeyPrefix     = "catchup_lock"
	defaultTimeout           = 5 * time.Second
	lockTimeout              = 3 * time.Second
)

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

const claimRangeScript = `
	local key = KEYS[1]
	local lockPrefix = ARGV[1]
	local lockExpiration = tonumber(ARGV[2])

	local entries = redis.call('HGETALL', key)
	for i = 1, #entries, 2 do
		local field = entries[i]
		local value = entries[i + 1]
		local startText, endText = string.match(field, '^(%d+)%-(%d+)$')
		if startText and endText then
			local startNum = tonumber(startText)
			local endNum = tonumber(endText)
			local currentNum = tonumber(value)

			if startNum and endNum and currentNum then
				if currentNum > endNum then
					currentNum = endNum
				end
				if currentNum + 1 < startNum then
					currentNum = startNum - 1
				end

				local lockKey = lockPrefix .. field
				local locked = redis.call('SET', lockKey, 'locked', 'NX', 'EX', lockExpiration)
				if locked then
					return {startNum, endNum, currentNum}
				end
			end
		end
	end

	return nil
`

type Store interface {
	SaveRanges(ctx context.Context, chain string, ranges []blockstore.CatchupRange) error
	SaveProgress(ctx context.Context, chain string, start, end, current uint64) error
	GetProgress(ctx context.Context, chain string) ([]blockstore.CatchupRange, error)
	GetNextRange(ctx context.Context, chain string) (*blockstore.CatchupRange, error)
	DeleteRange(ctx context.Context, chain string, start, end uint64) error
}

type noopStore struct{}

type catchupStore struct {
	redisClient infra.RedisClient
	addScript   *redis.Script
	claimScript *redis.Script
}

func New(redisClient infra.RedisClient) Store {
	if redisClient == nil || redisClient.GetClient() == nil {
		return noopStore{}
	}
	return &catchupStore{
		redisClient: redisClient,
		addScript:   redis.NewScript(addRangesScript),
		claimScript: redis.NewScript(claimRangeScript),
	}
}

func (noopStore) SaveRanges(context.Context, string, []blockstore.CatchupRange) error { return nil }
func (noopStore) SaveProgress(context.Context, string, uint64, uint64, uint64) error  { return nil }
func (noopStore) GetProgress(context.Context, string) ([]blockstore.CatchupRange, error) {
	return nil, nil
}
func (noopStore) GetNextRange(context.Context, string) (*blockstore.CatchupRange, error) {
	return nil, nil
}
func (noopStore) DeleteRange(context.Context, string, uint64, uint64) error { return nil }

func composeKey(chain string) string {
	return fmt.Sprintf("%s:%s", catchupProgressKeyPrefix, chain)
}

func composeField(start, end uint64) string {
	return fmt.Sprintf("%d-%d", start, end)
}

func composeLockKey(chain string, start, end uint64) string {
	return fmt.Sprintf("%s:%s:%d-%d", catchupLockKeyPrefix, chain, start, end)
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
	for _, r := range normalizeRanges(ranges) {
		if _, err := s.addScript.Run(
			ctx,
			s.redisClient.GetClient(),
			[]string{key},
			r.Start,
			r.End,
			r.Current,
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

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	values, err := s.redisClient.GetClient().HGetAll(ctx, composeKey(chain)).Result()
	if err != nil {
		return nil, fmt.Errorf("get catchup progress: %w", err)
	}

	return parseProgressMap(values), nil
}

func (s *catchupStore) GetNextRange(
	ctx context.Context,
	chain string,
) (*blockstore.CatchupRange, error) {
	if chain == "" {
		return nil, errors.New("chain name is required")
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	result, err := s.claimScript.Run(
		ctx,
		s.redisClient.GetClient(),
		[]string{composeKey(chain)},
		fmt.Sprintf("%s:%s:", catchupLockKeyPrefix, chain),
		int(lockTimeout.Seconds()),
	).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim catchup range: %w", err)
	}

	if result == nil {
		return nil, nil
	}

	values, ok := result.([]interface{})
	if !ok || len(values) != 3 {
		return nil, fmt.Errorf("unexpected claim result type: %T", result)
	}

	start, ok := toUint64(values[0])
	if !ok {
		return nil, fmt.Errorf("invalid claim start type: %T", values[0])
	}
	end, ok := toUint64(values[1])
	if !ok {
		return nil, fmt.Errorf("invalid claim end type: %T", values[1])
	}
	current, ok := toUint64(values[2])
	if !ok {
		return nil, fmt.Errorf("invalid claim current type: %T", values[2])
	}

	claimed := blockstore.CatchupRange{Start: start, End: end, Current: current}
	return &claimed, nil
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
	field := composeField(start, end)
	pipe := s.redisClient.GetClient().Pipeline()
	delCmd := pipe.HDel(ctx, key, field)
	lenCmd := pipe.HLen(ctx, key)
	pipe.Del(ctx, composeLockKey(chain, start, end))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete catchup range: %w", err)
	}
	if delCmd.Val() == 0 {
		return nil
	}
	if lenCmd.Val() == 0 {
		if err := s.redisClient.GetClient().Del(ctx, key).Err(); err != nil {
			return fmt.Errorf("cleanup catchup key: %w", err)
		}
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
		ranges = append(ranges, blockstore.CatchupRange{
			Start:   start,
			End:     end,
			Current: current,
		})
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

func normalizeRanges(ranges []blockstore.CatchupRange) []blockstore.CatchupRange {
	filtered := make([]blockstore.CatchupRange, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == 0 || r.End < r.Start {
			continue
		}
		if r.Current > r.End {
			r.Current = r.End
		}
		if r.Current+1 < r.Start {
			r.Current = r.Start - 1
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		return nil
	}

	slices.SortFunc(filtered, func(a, b blockstore.CatchupRange) int {
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

	merged := []blockstore.CatchupRange{filtered[0]}
	for _, next := range filtered[1:] {
		last := &merged[len(merged)-1]
		if next.Start > last.End+1 {
			merged = append(merged, next)
			continue
		}

		if next.End > last.End {
			last.End = next.End
		}
		if next.Current < last.Current {
			last.Current = next.Current
		}
		if last.Current+1 < last.Start {
			last.Current = last.Start - 1
		}
		if last.Current > last.End {
			last.Current = last.End
		}
	}

	return merged
}

func toUint64(v interface{}) (uint64, bool) {
	switch n := v.(type) {
	case int64:
		return uint64(n), true
	case uint64:
		return n, true
	case string:
		parsed, err := strconv.ParseUint(n, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseUint(string(n), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
