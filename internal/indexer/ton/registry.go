package ton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/redis/go-redis/v9"
)

const (
	jettonMasterListKeyFormat    = "ton/jettons/%s/masters"
	jettonWalletMappingKeyFormat = "ton/jettons/%s/wallet_to_master"
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

	return r.GetInfo(masterAddr)
}

// RegisterWallet associates a Jetton wallet with its master address.
// This is typically called when processing a Jetton transfer for the first time.
func (r *ConfigBasedRegistry) RegisterWallet(walletAddress, masterAddress string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Only register if the master is in our supported list
	if _, ok := r.jettons[masterAddress]; ok {
		r.walletToMaster[walletAddress] = masterAddress
	}
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

// RedisJettonRegistry loads supported jettons from Redis and keeps an in-memory snapshot.
// Key format:
// - ton/jettons/<chain>/masters: JSON []JettonInfo
// - ton/jettons/<chain>/wallet_to_master: JSON map[string]string
type RedisJettonRegistry struct {
	redis     infra.RedisClient
	chainName string

	fallbackJettons []JettonInfo

	mu             sync.RWMutex
	jettons        map[string]JettonInfo
	walletToMaster map[string]string
}

func NewRedisJettonRegistry(chainName string, redisClient infra.RedisClient, fallback []JettonInfo) *RedisJettonRegistry {
	reg := &RedisJettonRegistry{
		redis:           redisClient,
		chainName:       chainName,
		fallbackJettons: append([]JettonInfo(nil), fallback...),
		jettons:         make(map[string]JettonInfo),
		walletToMaster:  make(map[string]string),
	}
	reg.applyFallback()
	return reg
}

func (r *RedisJettonRegistry) applyFallback() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.jettons = make(map[string]JettonInfo, len(r.fallbackJettons))
	r.walletToMaster = make(map[string]string)
	for _, j := range r.fallbackJettons {
		if j.MasterAddress == "" {
			continue
		}
		r.jettons[j.MasterAddress] = j
	}
}

func (r *RedisJettonRegistry) mastersKey() string {
	return fmt.Sprintf(jettonMasterListKeyFormat, r.chainName)
}

func (r *RedisJettonRegistry) walletMappingKey() string {
	return fmt.Sprintf(jettonWalletMappingKeyFormat, r.chainName)
}

// Reload refreshes the registry snapshot from Redis.
func (r *RedisJettonRegistry) Reload(_ context.Context) error {
	if r.redis == nil {
		r.applyFallback()
		return nil
	}

	nextJettons := make(map[string]JettonInfo, len(r.fallbackJettons))
	for _, j := range r.fallbackJettons {
		if j.MasterAddress == "" {
			continue
		}
		nextJettons[j.MasterAddress] = j
	}
	nextWallets := make(map[string]string)

	masterRaw, err := r.redis.Get(r.mastersKey())
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("get jetton masters from redis: %w", err)
	}
	if err == nil && strings.TrimSpace(masterRaw) != "" {
		var masters []JettonInfo
		if unmarshalErr := json.Unmarshal([]byte(masterRaw), &masters); unmarshalErr != nil {
			return fmt.Errorf("unmarshal jetton masters: %w", unmarshalErr)
		}
		for _, j := range masters {
			if j.MasterAddress == "" {
				continue
			}
			nextJettons[j.MasterAddress] = j
		}
	}

	walletRaw, err := r.redis.Get(r.walletMappingKey())
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("get jetton wallet mapping from redis: %w", err)
	}
	if err == nil && strings.TrimSpace(walletRaw) != "" {
		if unmarshalErr := json.Unmarshal([]byte(walletRaw), &nextWallets); unmarshalErr != nil {
			return fmt.Errorf("unmarshal jetton wallet mapping: %w", unmarshalErr)
		}
	}

	r.mu.Lock()
	r.jettons = nextJettons
	r.walletToMaster = nextWallets
	r.mu.Unlock()
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
	r.mu.RLock()
	masterAddress, ok := r.walletToMaster[walletAddress]
	if !ok {
		r.mu.RUnlock()
		return nil, false
	}
	info, ok := r.jettons[masterAddress]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return &info, true
}

func (r *RedisJettonRegistry) RegisterWallet(walletAddress, masterAddress string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.jettons[masterAddress]; ok {
		r.walletToMaster[walletAddress] = masterAddress
	}
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
