package ton

import (
	"sync"
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
