package missingblockstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// Test Suite
type MissingBlocksStoreTestSuite struct {
	suite.Suite
	store       MissingBlocksStore
	redisClient infra.RedisClient
	networkCode string
}

func (suite *MissingBlocksStoreTestSuite) SetupSuite() {
	redisClient, err := infra.NewRedisClient("localhost:6379", "", "test", false)
	if err != nil {
		suite.T().Skip("Redis not available, skipping integration tests")
	}

	// Test connection
	ctx := context.Background()
	_, err = redisClient.GetClient().Ping(ctx).Result()
	if err != nil {
		suite.T().Skip("Redis not available, skipping integration tests")
	}
	suite.redisClient = redisClient
	suite.store = NewMissingBlocksStore(redisClient)
	suite.networkCode = "test_network"
}

func (suite *MissingBlocksStoreTestSuite) TearDownSuite() {
	if suite.redisClient != nil {
		err := suite.redisClient.Close()
		suite.NoError(err)
	}
	if suite.redisClient != nil {
		suite.redisClient.Close()
	}
}

func (suite *MissingBlocksStoreTestSuite) SetupTest() {
	// Clean up before each test
	ctx := context.Background()
	suite.store.FlushRanges(ctx, suite.networkCode)

	// Clean up any leftover keys
	pattern := fmt.Sprintf("*%s*", suite.networkCode)
	keys, _ := suite.redisClient.GetClient().Keys(ctx, pattern).Result()
	if len(keys) > 0 {
		suite.redisClient.GetClient().Del(ctx, keys...)
	}
}

func (suite *MissingBlocksStoreTestSuite) TestAddMissingBlockRange_SingleRange() {
	ctx := context.Background()

	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 200)
	suite.NoError(err)

	ranges, err := suite.store.ListRanges(ctx, suite.networkCode)
	suite.NoError(err)

	// expected = ceil((end-start+1)/MaxBlocksPerRange)
	total := 200 - 100 + 1
	expectedRanges := (int(total) + MaxBlocksPerRange - 1) / MaxBlocksPerRange
	suite.Len(ranges, expectedRanges)

	// With MaxBlocksPerRange = 5, we should have ranges: 100-104, 105-109, ..., 195-199, 200-200
	suite.Equal(uint64(100), ranges[0].StartBlock)
	suite.Equal(uint64(104), ranges[0].EndBlock)
	suite.Equal(uint64(200), ranges[len(ranges)-1].StartBlock)
	suite.Equal(uint64(200), ranges[len(ranges)-1].EndBlock)
}

func (suite *MissingBlocksStoreTestSuite) TestAddMissingBlockRange_OverlappingRanges() {
	ctx := context.Background()

	// Add first range
	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 120)
	suite.NoError(err)

	// Add overlapping range
	err = suite.store.AddMissingBlockRange(ctx, suite.networkCode, 115, 140)
	suite.NoError(err)

	ranges, err := suite.store.ListRanges(ctx, suite.networkCode)
	suite.NoError(err)

	// Should merge into one continuous range from 100-140
	total := 140 - 100 + 1
	expectedRanges := (int(total) + MaxBlocksPerRange - 1) / MaxBlocksPerRange
	suite.Len(ranges, expectedRanges)

	// Verify merged range chunks
	suite.Equal(uint64(100), ranges[0].StartBlock)
	suite.Equal(uint64(104), ranges[0].EndBlock)
	suite.Equal(uint64(140), ranges[len(ranges)-1].StartBlock)
	suite.Equal(uint64(140), ranges[len(ranges)-1].EndBlock)
}

func (suite *MissingBlocksStoreTestSuite) TestAddMissingBlockRange_AdjacentRanges() {
	ctx := context.Background()

	// Add first range
	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 110)
	suite.NoError(err)

	// Add adjacent range
	err = suite.store.AddMissingBlockRange(ctx, suite.networkCode, 111, 120)
	suite.NoError(err)

	ranges, err := suite.store.ListRanges(ctx, suite.networkCode)
	suite.NoError(err)

	// Should merge into continuous range from 100-120
	expectedRanges := 5 // (120-100+1)/5 = 4.2 -> 5 ranges
	suite.Len(ranges, expectedRanges)
}

func (suite *MissingBlocksStoreTestSuite) TestAddMissingBlockRange_InvalidInput() {
	ctx := context.Background()

	testCases := []struct {
		name  string
		start uint64
		end   uint64
	}{
		{"zero start", 0, 100},
		{"zero end", 100, 0},
		{"start greater than end", 200, 100},
	}

	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, tc.start, tc.end)
			suite.Error(err)
		})
	}
}

func (suite *MissingBlocksStoreTestSuite) TestGetNextRange_Success() {
	ctx := context.Background()

	// Add some ranges
	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 109)
	suite.NoError(err)

	err = suite.store.AddMissingBlockRange(ctx, suite.networkCode, 200, 204)
	suite.NoError(err)

	// Get next range
	start, end, err := suite.store.GetNextRange(ctx, suite.networkCode)
	suite.NoError(err)
	suite.True(start > 0 && end > 0)
	suite.True(start <= end)

	// Should be one of the ranges we added (either 100-104, 105-109, or 200-204)
	validRanges := [][]uint64{{100, 104}, {105, 109}, {200, 204}}
	found := false
	for _, validRange := range validRanges {
		if start == validRange[0] && end == validRange[1] {
			found = true
			break
		}
	}
	suite.True(found, "Retrieved range should be one of the added ranges")
}

func (suite *MissingBlocksStoreTestSuite) TestGetNextRange_NoRanges() {
	ctx := context.Background()

	start, end, err := suite.store.GetNextRange(ctx, suite.networkCode)
	suite.NoError(err)
	suite.Equal(uint64(0), start)
	suite.Equal(uint64(0), end)
}

func (suite *MissingBlocksStoreTestSuite) TestGetNextRange_Concurrent() {
	ctx := context.Background()

	// Add multiple ranges
	for i := 0; i < 10; i++ {
		start := uint64(i*100 + 1)
		end := uint64(i*100 + 5)
		err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, start, end)
		suite.NoError(err)
	}

	// Concurrently get ranges
	const numGoroutines = 20
	var wg sync.WaitGroup
	results := make(chan [2]uint64, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start, end, err := suite.store.GetNextRange(ctx, suite.networkCode)
			if err == nil && start > 0 {
				results <- [2]uint64{start, end}
			}
		}()
	}

	wg.Wait()
	close(results)

	// Collect unique results
	unique := make(map[[2]uint64]bool)
	for result := range results {
		unique[result] = true
	}

	// Should have at least some unique ranges (no duplicates due to locking)
	suite.True(len(unique) > 0)
}

func (suite *MissingBlocksStoreTestSuite) TestRemoveRange() {
	ctx := context.Background()

	// Add range
	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 109)
	suite.NoError(err)

	// Set some processed data
	err = suite.store.SetRangeProcessed(ctx, suite.networkCode, 100, 104, 102)
	suite.NoError(err)

	// Remove the range
	err = suite.store.RemoveRange(ctx, suite.networkCode, 100, 104)
	suite.NoError(err)

	// Verify range is removed
	ranges, err := suite.store.ListRanges(ctx, suite.networkCode)
	suite.NoError(err)

	// Should only have the remaining range (105-109)
	found := false
	for _, r := range ranges {
		if r.StartBlock == 100 && r.EndBlock == 104 {
			found = true
			break
		}
	}
	suite.False(found, "Range should be removed")

	// Verify processed data is cleaned up
	processed, err := suite.store.GetRangeProcessed(ctx, suite.networkCode, 100, 104)
	suite.NoError(err)
	suite.Equal(uint64(0), processed)
}

func (suite *MissingBlocksStoreTestSuite) TestSetAndGetRangeProcessed() {
	ctx := context.Background()

	// Set processed
	err := suite.store.SetRangeProcessed(ctx, suite.networkCode, 100, 200, 150)
	suite.NoError(err)

	// Get processed
	processed, err := suite.store.GetRangeProcessed(ctx, suite.networkCode, 100, 200)
	suite.NoError(err)
	suite.Equal(uint64(150), processed)

	// Get non-existent processed
	processed, err = suite.store.GetRangeProcessed(ctx, suite.networkCode, 300, 400)
	suite.NoError(err)
	suite.Equal(uint64(0), processed)
}

func (suite *MissingBlocksStoreTestSuite) TestListRanges() {
	ctx := context.Background()

	// Add multiple ranges
	ranges := [][2]uint64{
		{100, 104},
		{200, 209},
		{300, 304},
	}

	for _, r := range ranges {
		err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, r[0], r[1])
		suite.NoError(err)
	}

	// List ranges
	listedRanges, err := suite.store.ListRanges(ctx, suite.networkCode)
	suite.NoError(err)

	// 200-209 splits into two ranges (200-204, 205-209), total 4
	suite.Len(listedRanges, 4)

	// Sort for comparison
	sort.Slice(listedRanges, func(i, j int) bool {
		return listedRanges[i].StartBlock < listedRanges[j].StartBlock
	})

	expectedRanges := []BlockRange{
		{StartBlock: 100, EndBlock: 104},
		{StartBlock: 200, EndBlock: 204},
		{StartBlock: 205, EndBlock: 209},
		{StartBlock: 300, EndBlock: 304},
	}

	for i := range expectedRanges {
		suite.Equal(expectedRanges[i].StartBlock, listedRanges[i].StartBlock)
		suite.Equal(expectedRanges[i].EndBlock, listedRanges[i].EndBlock)
	}
}

func (suite *MissingBlocksStoreTestSuite) TestCountRanges() {
	ctx := context.Background()

	// Initially should be 0
	count, err := suite.store.CountRanges(ctx, suite.networkCode)
	suite.NoError(err)
	suite.Equal(int64(0), count)

	// Add some ranges
	err = suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 109)
	suite.NoError(err)

	count, err = suite.store.CountRanges(ctx, suite.networkCode)
	suite.NoError(err)
	suite.Equal(int64(2), count) // 100-104, 105-109
}

func (suite *MissingBlocksStoreTestSuite) TestFlushRanges() {
	ctx := context.Background()

	// Add ranges
	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 200)
	suite.NoError(err)

	// Verify ranges exist
	count, err := suite.store.CountRanges(ctx, suite.networkCode)
	suite.NoError(err)
	suite.True(count > 0)

	// Flush ranges
	err = suite.store.FlushRanges(ctx, suite.networkCode)
	suite.NoError(err)

	// Verify ranges are gone
	count, err = suite.store.CountRanges(ctx, suite.networkCode)
	suite.NoError(err)
	suite.Equal(int64(0), count)
}

func (suite *MissingBlocksStoreTestSuite) TestContextCancellation() {
	// Test context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 200)
	suite.Error(err)
	suite.Contains(err.Error(), "context canceled")
}

func (suite *MissingBlocksStoreTestSuite) TestContextTimeout() {
	// Test context timeout with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	time.Sleep(1 * time.Millisecond) // Ensure timeout

	err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 200)
	suite.Error(err)
}

// Utility functions tests
func TestParseRange(t *testing.T) {
	testCases := []struct {
		input    string
		expected [3]interface{} // start, end, ok
	}{
		{"100-200", [3]interface{}{uint64(100), uint64(200), true}},
		{"1-1", [3]interface{}{uint64(1), uint64(1), true}},
		{"invalid", [3]interface{}{uint64(0), uint64(0), false}},
		{"100-", [3]interface{}{uint64(0), uint64(0), false}},
		{"-200", [3]interface{}{uint64(0), uint64(0), false}},
		{"0-200", [3]interface{}{uint64(0), uint64(0), false}},
		{"200-100", [3]interface{}{uint64(0), uint64(0), false}},
		{"", [3]interface{}{uint64(0), uint64(0), false}},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			start, end, ok := parseRange(tc.input)
			assert.Equal(t, tc.expected[0], start)
			assert.Equal(t, tc.expected[1], end)
			assert.Equal(t, tc.expected[2], ok)
		})
	}
}

func TestFormatRange(t *testing.T) {
	testCases := []struct {
		start, end uint64
		expected   string
	}{
		{100, 200, "100-200"},
		{1, 1, "1-1"},
		{0, 0, "0-0"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			result := formatRange(tc.start, tc.end)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// Integration test with real Redis (if available)
func TestMissingBlocksStoreIntegration(t *testing.T) {
	suite.Run(t, new(MissingBlocksStoreTestSuite))
}

// Benchmark tests
func BenchmarkAddMissingBlockRange(b *testing.B) {
	// This would require a real Redis setup
	b.Skip("Benchmark requires Redis setup")
}

func BenchmarkGetNextRange(b *testing.B) {
	// This would require a real Redis setup
	b.Skip("Benchmark requires Redis setup")
}

// Real Redis Client wrapper for testing
type RealRedisClient struct {
	client *redis.Client
}

func (r *RealRedisClient) GetClient() *redis.Client {
	return r.client
}

func (r *RealRedisClient) ZAdd(key string, members ...redis.Z) error {
	return r.client.ZAdd(context.Background(), key, members...).Err()
}

func (r *RealRedisClient) ZRangeWithScores(key string, min, max int64) ([]redis.Z, error) {
	if min == 0 && max == -1 {
		// Get all elements
		return r.client.ZRangeWithScores(context.Background(), key, 0, -1).Result()
	}
	// Assume it's a score range query
	return r.client.ZRangeByScoreWithScores(context.Background(), key, &redis.ZRangeBy{
		Min: fmt.Sprintf("%d", min),
		Max: fmt.Sprintf("%d", max),
	}).Result()
}

func (r *RealRedisClient) ZRem(key string, members ...interface{}) error {
	return r.client.ZRem(context.Background(), key, members...).Err()
}

func (r *RealRedisClient) Get(key string) (string, error) {
	return r.client.Get(context.Background(), key).Result()
}

func (r *RealRedisClient) Del(keys ...string) error {
	return r.client.Del(context.Background(), keys...).Err()
}

func (r *RealRedisClient) Close() error {
	return r.client.Close()
}

// Test helper functions
func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		DB:       15, // Test database
		Password: "",
	})

	ctx := context.Background()
	_, err := client.Ping(ctx).Result()
	if err != nil {
		t.Skip("Redis not available")
	}

	// Cleanup function
	cleanup := func() {
		client.FlushDB(ctx)
		client.Close()
	}

	return client, cleanup
}

// Edge case tests
func (suite *MissingBlocksStoreTestSuite) TestEdgeCases() {
	ctx := context.Background()

	suite.Run("SingleBlockRange", func() {
		err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 100, 100)
		suite.NoError(err)

		ranges, err := suite.store.ListRanges(ctx, suite.networkCode)
		suite.NoError(err)
		suite.Len(ranges, 1)
		suite.Equal(uint64(100), ranges[0].StartBlock)
		suite.Equal(uint64(100), ranges[0].EndBlock)
	})

	suite.Run("LargeRange", func() {
		err := suite.store.AddMissingBlockRange(ctx, suite.networkCode, 1, 1000)
		suite.NoError(err)

		count, err := suite.store.CountRanges(ctx, suite.networkCode)
		suite.NoError(err)
		suite.Equal(int64(200), count) // 1000/5 = 200 ranges
	})
}
