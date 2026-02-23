package ton

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
)

const (
	assetCacheKeyPatternFormat = "assetcache:%s:*"
	redisScanCount             = int64(200)
	walletMappingTTL           = 24 * time.Hour
	walletPruneInterval        = 5 * time.Minute
)

// ConfigBasedRegistry implements JettonRegistry with a static list of Jettons.
// Wallet-to-master mappings are cached as they're discovered.
type ConfigBasedRegistry struct {
	jettons        map[string]JettonInfo // key: master address
	walletToMaster map[string]string     // key: wallet address -> master address
	mu             sync.RWMutex
}

// NewConfigBasedRegistry creates a registry from a list of supported Jettons.
func NewConfigBasedRegistry(jettons []JettonInfo) *ConfigBasedRegistry {
	m := make(map[string]JettonInfo)
	for _, j := range jettons {
		m[j.MasterAddress] = j
	}
	return &ConfigBasedRegistry{
		jettons:        m,
		walletToMaster: make(map[string]string),
	}
}

// IsSupported checks if a Jetton wallet belongs to a supported Jetton.
func (r *ConfigBasedRegistry) IsSupported(walletAddress string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.walletToMaster[walletAddress]
	return ok
}

// GetInfo returns info for a Jetton by its master address.
func (r *ConfigBasedRegistry) GetInfo(masterAddress string) (*JettonInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.jettons[masterAddress]
	if !ok {
		return nil, false
	}
	return &info, true
}

// GetInfoByWallet returns info for a Jetton by a wallet address.
func (r *ConfigBasedRegistry) GetInfoByWallet(walletAddress string) (*JettonInfo, bool) {
	r.mu.RLock()
	masterAddr, ok := r.walletToMaster[walletAddress]
	r.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if info, ok := r.GetInfo(masterAddr); ok {
		return info, true
	}
	// Fallback: return master address even when token metadata is not preconfigured.
	return &JettonInfo{MasterAddress: masterAddr}, true
}

// RegisterWallet associates a Jetton wallet with its master address.
// This is typically called when processing a Jetton transfer for the first time.
func (r *ConfigBasedRegistry) RegisterWallet(walletAddress, masterAddress string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.walletToMaster[walletAddress] = masterAddress
}

// List returns all supported Jettons.
func (r *ConfigBasedRegistry) List() []JettonInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]JettonInfo, 0, len(r.jettons))
	for _, j := range r.jettons {
		result = append(result, j)
	}
	return result
}

// RedisJettonRegistry loads supported jettons from Redis asset cache and keeps an in-memory snapshot.
// Key format:
// - assetcache:<network_code>:<asset_code> (source of supported jettons)
// Wallet-to-master mapping is runtime in-memory only.
type RedisJettonRegistry struct {
	redis       infra.RedisClient
	networkCode string

	mu             sync.RWMutex
	jettons        map[string]JettonInfo
	walletToMaster map[string]string
	walletSeenAt   map[string]time.Time
	lastPruneAt    time.Time
}

func NewRedisJettonRegistry(networkCode string, redisClient infra.RedisClient) *RedisJettonRegistry {
	reg := &RedisJettonRegistry{
		redis:          redisClient,
		networkCode:    strings.TrimSpace(networkCode),
		jettons:        make(map[string]JettonInfo),
		walletToMaster: make(map[string]string),
		walletSeenAt:   make(map[string]time.Time),
	}
	return reg
}

// Reload refreshes the registry snapshot from Redis.
func (r *RedisJettonRegistry) Reload(ctx context.Context) error {
	if r.redis == nil {
		r.mu.Lock()
		r.jettons = make(map[string]JettonInfo)
		r.walletToMaster = make(map[string]string)
		r.walletSeenAt = make(map[string]time.Time)
		r.lastPruneAt = time.Time{}
		r.mu.Unlock()
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	nextJettons := make(map[string]JettonInfo)
	nextWallets := make(map[string]string)
	nextSeenAt := make(map[string]time.Time)
	r.mu.RLock()
	maps.Copy(nextWallets, r.walletToMaster)
	maps.Copy(nextSeenAt, r.walletSeenAt)
	lastPruneAt := r.lastPruneAt
	r.mu.RUnlock()

	if err := r.loadJettonsFromAssetCache(ctx, nextJettons); err != nil {
		return err
	}

	r.mu.Lock()
	r.jettons = nextJettons
	r.walletToMaster = nextWallets
	r.walletSeenAt = nextSeenAt
	r.lastPruneAt = lastPruneAt
	r.pruneExpiredLocked(time.Now())
	r.mu.Unlock()
	return nil
}

func (r *RedisJettonRegistry) loadJettonsFromAssetCache(
	ctx context.Context,
	nextJettons map[string]JettonInfo,
) error {
	client := r.redis.GetClient()
	if client == nil {
		return fmt.Errorf("redis client is nil")
	}

	pattern := fmt.Sprintf(assetCacheKeyPatternFormat, r.networkCode)
	if err := r.scanAssetCachePattern(ctx, client, pattern, nextJettons); err != nil {
		return fmt.Errorf("load asset cache by network code %q: %w", r.networkCode, err)
	}

	return nil
}

func (r *RedisJettonRegistry) scanAssetCachePattern(
	ctx context.Context,
	client *redis.Client,
	pattern string,
	nextJettons map[string]JettonInfo,
) error {
	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, redisScanCount).Result()
		if err != nil {
			return fmt.Errorf("scan keys by pattern %q: %w", pattern, err)
		}

		for _, key := range keys {
			masterAddress, err := extractAssetCodeFromKey(key)
			if err != nil {
				continue
			}
			if strings.EqualFold(masterAddress, "native") {
				continue
			}

			raw, err := client.Get(ctx, key).Result()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					continue
				}
				return fmt.Errorf("get asset cache %q: %w", key, err)
			}

			meta, ok := parseAssetCacheMetadata(raw)
			if !ok || meta.IsNativeAsset {
				continue
			}

			normalizedMaster := NormalizeTONAddressRaw(masterAddress)
			if normalizedMaster == "" {
				continue
			}

			nextJettons[normalizedMaster] = JettonInfo{
				MasterAddress: normalizedMaster,
				Symbol:        meta.Symbol,
				Decimals:      meta.Decimals,
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

func (r *RedisJettonRegistry) IsSupported(walletAddress string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	masterAddress, ok := r.walletToMaster[walletAddress]
	if !ok {
		return false
	}
	_, ok = r.jettons[masterAddress]
	return ok
}

func (r *RedisJettonRegistry) GetInfo(masterAddress string) (*JettonInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.jettons[masterAddress]
	if !ok {
		return nil, false
	}
	return &info, true
}

func (r *RedisJettonRegistry) GetInfoByWallet(walletAddress string) (*JettonInfo, bool) {
	now := time.Now()
	r.mu.Lock()
	r.pruneExpiredLocked(now)
	masterAddress, ok := r.walletToMaster[walletAddress]
	if !ok {
		r.mu.Unlock()
		return nil, false
	}
	r.walletSeenAt[walletAddress] = now
	info, ok := r.jettons[masterAddress]
	r.mu.Unlock()
	if !ok {
		// Fallback: return master address even when token metadata is not in masters list.
		return &JettonInfo{MasterAddress: masterAddress}, true
	}
	return &info, true
}

func (r *RedisJettonRegistry) RegisterWallet(walletAddress, masterAddress string) {
	if strings.TrimSpace(walletAddress) == "" || strings.TrimSpace(masterAddress) == "" {
		return
	}
	if normalizedMaster := NormalizeTONAddressRaw(masterAddress); normalizedMaster != "" {
		masterAddress = normalizedMaster
	}

	now := time.Now()
	r.mu.Lock()
	r.pruneExpiredLocked(now)
	r.walletToMaster[walletAddress] = masterAddress
	r.walletSeenAt[walletAddress] = now
	r.mu.Unlock()
}

func (r *RedisJettonRegistry) List() []JettonInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]JettonInfo, 0, len(r.jettons))
	for _, j := range r.jettons {
		result = append(result, j)
	}
	return result
}

type assetCacheMetadata struct {
	AssetID       string `json:"asset_id"`
	Symbol        string `json:"symbol"`
	Decimals      int    `json:"decimals"`
	IsNativeAsset bool   `json:"is_native_asset"`
}

func extractAssetCodeFromKey(key string) (string, error) {
	parts := strings.Split(key, ":")
	if len(parts) < 3 || parts[0] != "assetcache" {
		return "", fmt.Errorf("invalid asset cache key: %s", key)
	}
	return strings.Join(parts[2:], ":"), nil
}

func parseAssetCacheMetadata(raw string) (*assetCacheMetadata, bool) {
	return parseAssetCacheMetadataBytes([]byte(strings.TrimSpace(raw)))
}

func parseAssetCacheMetadataBytes(payload []byte) (*assetCacheMetadata, bool) {
	payload = []byte(strings.TrimSpace(string(payload)))
	if len(payload) == 0 {
		return nil, false
	}

	meta := &assetCacheMetadata{}
	if err := json.Unmarshal(payload, meta); err == nil {
		if isValidAssetCacheMetadata(meta) {
			return meta, true
		}
		return nil, false
	}

	var wrapped string
	if err := json.Unmarshal(payload, &wrapped); err == nil {
		return parseAssetCacheMetadataBytes([]byte(wrapped))
	}

	if decoded, ok := decodeBase64(payload); ok {
		return parseAssetCacheMetadataBytes(decoded)
	}

	return nil, false
}

func isValidAssetCacheMetadata(meta *assetCacheMetadata) bool {
	if meta == nil {
		return false
	}
	// Keep parser strict enough to avoid treating arbitrary JSON as asset metadata.
	return strings.TrimSpace(meta.Symbol) != "" || strings.TrimSpace(meta.AssetID) != ""
}

func decodeBase64(payload []byte) ([]byte, bool) {
	text := strings.TrimSpace(string(payload))
	if text == "" {
		return nil, false
	}

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}

	for _, enc := range encodings {
		decoded, err := enc.DecodeString(text)
		if err != nil || len(decoded) == 0 {
			continue
		}
		return decoded, true
	}
	return nil, false
}

func (r *RedisJettonRegistry) pruneExpiredLocked(now time.Time) {
	if !r.lastPruneAt.IsZero() && now.Sub(r.lastPruneAt) < walletPruneInterval {
		return
	}
	r.lastPruneAt = now

	if len(r.walletToMaster) == 0 {
		return
	}

	expireBefore := now.Add(-walletMappingTTL)
	for wallet, seenAt := range r.walletSeenAt {
		if seenAt.After(expireBefore) {
			continue
		}
		delete(r.walletSeenAt, wallet)
		delete(r.walletToMaster, wallet)
	}
}
