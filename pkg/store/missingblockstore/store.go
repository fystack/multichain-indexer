package missingblockstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
)

type BlockRange struct {
	StartBlock uint64
	EndBlock   uint64
}

const (
	missingBlocksKeyPrefix = "missing_blocks"
	processedKeyPrefix     = "processed"
	lockKeyPrefix          = "processing"

	MaxBlocksPerRange = 5

	// Timeouts
	defaultTimeout = 5 * time.Second
	lockTimeout    = 3 * time.Second
	processedTTL   = 5 * time.Minute
)

// Pre-compiled Lua scripts for atomic operations
const (
	// Atomically claim and lock a range
	claimRangeScript = `
		local ranges = redis.call('ZRANGE', KEYS[1], 0, -1)
		local lockPrefix = ARGV[1]
		local lockExpiration = tonumber(ARGV[2])
		
		for i, rangeStr in ipairs(ranges) do
			local lockKey = lockPrefix .. rangeStr
			local lockSet = redis.call('SET', lockKey, "locked", "NX", "EX", lockExpiration)
			if lockSet then
				return rangeStr
			end
		end
		return nil
	`

	// Atomically add ranges with overlap handling
	addRangesScript = `
		local key = KEYS[1]
		local newStart = tonumber(ARGV[1])
		local newEnd = tonumber(ARGV[2])
		local maxBlocksPerRange = tonumber(ARGV[3])
		
		-- Get overlapping ranges
		local minScore = newStart - 1
		local maxScore = newEnd + 1
		local ranges = redis.call('ZRANGEBYSCORE', key, minScore, maxScore)
		
		-- Process overlaps
		local finalStart = newStart
		local finalEnd = newEnd
		
		for i, rangeStr in ipairs(ranges) do
			local existingStart, existingEnd = string.match(rangeStr, "(%d+)-(%d+)")
			existingStart = tonumber(existingStart)
			existingEnd = tonumber(existingEnd)
			
			if existingEnd + 1 >= newStart and existingStart <= newEnd + 1 then
				if existingStart < finalStart then
					finalStart = existingStart
				end
				if existingEnd > finalEnd then
					finalEnd = existingEnd
				end
				redis.call('ZREM', key, rangeStr)
			end
		end
		
		-- Add split ranges
		local from = finalStart
		while from <= finalEnd do
			local to = math.min(from + maxBlocksPerRange - 1, finalEnd)
			local rangeStr = from .. "-" .. to
			redis.call('ZADD', key, from, rangeStr)
			from = to + 1
		end
		
		return {finalStart, finalEnd}
	`
)

type MissingBlocksStore interface {
	AddMissingBlockRange(ctx context.Context, networkCode string, start, end uint64) error
	GetNextRange(ctx context.Context, networkCode string) (uint64, uint64, error)
	RemoveRange(ctx context.Context, networkCode string, start, end uint64) error
	SetRangeProcessed(ctx context.Context, networkCode string, start, end, current uint64) error
	GetRangeProcessed(ctx context.Context, networkCode string, start, end uint64) (uint64, error)
	ListRanges(ctx context.Context, networkCode string) ([]BlockRange, error)
	CountRanges(ctx context.Context, networkCode string) (int64, error)
	FlushRanges(ctx context.Context, networkCode string) error
}

type missingBlocksStore struct {
	redisClient infra.RedisClient
	// Pre-compiled scripts for better performance
	claimScript *redis.Script
	addScript   *redis.Script
}

func NewMissingBlocksStore(redisClient infra.RedisClient) MissingBlocksStore {
	return &missingBlocksStore{
		redisClient: redisClient,
		claimScript: redis.NewScript(claimRangeScript),
		addScript:   redis.NewScript(addRangesScript),
	}
}

// Key composition methods with consistent formatting
func (m *missingBlocksStore) composeKey(networkCode string) string {
	return fmt.Sprintf("%s:%s", missingBlocksKeyPrefix, networkCode)
}

func (m *missingBlocksStore) composeProcessedKey(networkCode string, start, end uint64) string {
	return fmt.Sprintf("%s:%s:%d-%d", processedKeyPrefix, networkCode, start, end)
}

func (m *missingBlocksStore) composeProcessingLockKey(networkCode string, start, end uint64) string {
	return fmt.Sprintf("%s:%s:%d-%d", lockKeyPrefix, networkCode, start, end)
}

func (m *missingBlocksStore) AddMissingBlockRange(ctx context.Context, networkCode string, start, end uint64) error {
	if start == 0 || end == 0 || start > end {
		return fmt.Errorf("invalid range: start=%d, end=%d", start, end)
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	key := m.composeKey(networkCode)

	// Use Lua script for atomic operation
	result, err := m.addScript.Run(ctx, m.redisClient.GetClient(),
		[]string{key},
		start, end, MaxBlocksPerRange).Result()
	if err != nil {
		return fmt.Errorf("failed to add missing block range: %w", err)
	}

	// Optional: log the actual range that was added
	if resultSlice, ok := result.([]interface{}); ok && len(resultSlice) == 2 {
		actualStart := resultSlice[0]
		actualEnd := resultSlice[1]
		_ = actualStart // Can be used for logging
		_ = actualEnd   // Can be used for logging
	}

	return nil
}

func (m *missingBlocksStore) GetNextRange(ctx context.Context, networkCode string) (uint64, uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	key := m.composeKey(networkCode)
	lockPrefix := fmt.Sprintf("%s:%s:", lockKeyPrefix, networkCode)
	lockExpiration := int(constant.RangeProcessingTimeout.Seconds())

	// Use pre-compiled Lua script
	result, err := m.claimScript.Run(ctx, m.redisClient.GetClient(),
		[]string{key},
		lockPrefix, lockExpiration).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, 0, nil // No ranges available
		}
		return 0, 0, fmt.Errorf("failed to claim range: %w", err)
	}

	if result == nil {
		return 0, 0, nil // No available ranges
	}

	rangeStr, ok := result.(string)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected result type: %T", result)
	}

	start, end, ok := parseRange(rangeStr)
	if !ok {
		// Clean up invalid lock
		lockKey := fmt.Sprintf("%s%s", lockPrefix, rangeStr)
		_ = m.redisClient.GetClient().Del(ctx, lockKey).Err()
		return 0, 0, fmt.Errorf("failed to parse range: %s", rangeStr)
	}

	return start, end, nil
}

func (m *missingBlocksStore) RemoveRange(ctx context.Context, networkCode string, start, end uint64) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	// Use pipeline for efficient batch operations
	pipe := m.redisClient.GetClient().Pipeline()

	// Queue all operations
	key := m.composeKey(networkCode)
	rangeStr := formatRange(start, end)
	pipe.ZRem(ctx, key, rangeStr)

	processedKey := m.composeProcessedKey(networkCode, start, end)
	pipe.Del(ctx, processedKey)

	lockKey := m.composeProcessingLockKey(networkCode, start, end)
	pipe.Del(ctx, lockKey)

	// Execute pipeline
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to remove range: %w", err)
	}

	return nil
}

func (m *missingBlocksStore) SetRangeProcessed(ctx context.Context, networkCode string, start, end, current uint64) error {
	ctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	key := m.composeProcessedKey(networkCode, start, end)

	if err := m.redisClient.GetClient().Set(ctx, key, current, processedTTL).Err(); err != nil {
		return fmt.Errorf("failed to set range processed: %w", err)
	}

	return nil
}

func (m *missingBlocksStore) GetRangeProcessed(ctx context.Context, networkCode string, start, end uint64) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	key := m.composeProcessedKey(networkCode, start, end)
	value, err := m.redisClient.GetClient().Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil // Not found, return 0
		}
		return 0, fmt.Errorf("failed to get range processed: %w", err)
	}

	processed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse processed block: %w", err)
	}

	return processed, nil
}

func (m *missingBlocksStore) ListRanges(ctx context.Context, networkCode string) ([]BlockRange, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	key := m.composeKey(networkCode)

	// Use ZRANGE with scores for better performance
	zRanges, err := m.redisClient.GetClient().ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ranges: %w", err)
	}

	ranges := make([]BlockRange, 0, len(zRanges))
	for _, z := range zRanges {
		rangeStr, ok := z.Member.(string)
		if !ok {
			continue
		}

		start, end, ok := parseRange(rangeStr)
		if !ok {
			continue
		}

		ranges = append(ranges, BlockRange{
			StartBlock: start,
			EndBlock:   end,
		})
	}

	return ranges, nil
}

func (m *missingBlocksStore) CountRanges(ctx context.Context, networkCode string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	key := m.composeKey(networkCode)
	count, err := m.redisClient.GetClient().ZCard(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to count ranges: %w", err)
	}

	return count, nil
}

func (m *missingBlocksStore) FlushRanges(ctx context.Context, networkCode string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	key := m.composeKey(networkCode)
	if err := m.redisClient.GetClient().Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to flush ranges: %w", err)
	}

	return nil
}

func formatRange(start, end uint64) string {
	return fmt.Sprintf("%d-%d", start, end)
}

func parseRange(s string) (uint64, uint64, bool) {
	var start, end uint64
	n, err := fmt.Sscanf(s, "%d-%d", &start, &end)
	if err != nil || n != 2 {
		return 0, 0, false
	}
	if start == 0 || end == 0 || start > end {
		return 0, 0, false
	}
	return start, end, true
}
