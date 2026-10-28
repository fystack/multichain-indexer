package worker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/store/missingblockstore"
)

// Operators can change a chain's RPC nodes while the indexer runs, and queue block ranges for the manual worker
// to re-scan. Node changes are kept in the KV store under nodeOverridePrefix+chain and applied again on start,
// over the nodes in config.

const nodeOverridePrefix = "node_overrides/"

var (
	ErrUnknownChain       = errors.New("unknown chain")
	ErrNodeControlMissing = errors.New("this chain's nodes can't be changed at runtime")
)

type nodeController struct {
	replace    func([]config.NodeConfig) error
	configured []config.NodeConfig
	overridden bool
}

var (
	nodeControllersMu sync.RWMutex
	nodeControllers   = map[string]*nodeController{}
)

// registerNodeController is called by the indexer builders (EVM, Tron, Bitcoin, Solana) for the shared indexer.
func registerNodeController(chainName string, replace func([]config.NodeConfig) error) {
	nodeControllersMu.Lock()
	defer nodeControllersMu.Unlock()
	controller, ok := nodeControllers[chainName]
	if !ok {
		controller = &nodeController{}
		nodeControllers[chainName] = controller
	}
	controller.replace = replace
}

func setConfiguredNodes(chainName string, nodes []config.NodeConfig) {
	nodeControllersMu.Lock()
	defer nodeControllersMu.Unlock()
	controller, ok := nodeControllers[chainName]
	if !ok {
		controller = &nodeController{}
		nodeControllers[chainName] = controller
	}
	controller.configured = append([]config.NodeConfig(nil), nodes...)
}

// applyNodeOverride swaps in the operator's nodes for the chain, if any were saved.
func applyNodeOverride(kv infra.KVStore, chainName string, chainCfg *config.ChainConfig) {
	if kv == nil {
		return
	}
	var urls []string
	found, err := kv.GetAny(nodeOverridePrefix+chainName, &urls)
	if err != nil || !found || len(urls) == 0 {
		return
	}
	chainCfg.Nodes = nodesFromURLs(urls)
	markOverridden(chainName, true)
	logger.Info("Using operator-set RPC nodes", "chain", chainName, "count", len(urls))
}

func nodesFromURLs(urls []string) []config.NodeConfig {
	nodes := make([]config.NodeConfig, 0, len(urls))
	for _, u := range urls {
		nodes = append(nodes, config.NodeConfig{URL: u})
	}
	return nodes
}

// ValidateNodeURLs accepts 1 to 10 absolute http(s) URLs.
func ValidateNodeURLs(urls []string) error {
	if len(urls) == 0 || len(urls) > 10 {
		return fmt.Errorf("give between 1 and 10 node URLs")
	}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("not an http(s) URL: %q", raw)
		}
	}
	return nil
}

// SetChainNodes replaces the chain's RPC nodes now and keeps them across restarts.
func (m *Manager) SetChainNodes(chainName string, urls []string) error {
	if err := ValidateNodeURLs(urls); err != nil {
		return err
	}
	controller, err := controllerFor(chainName)
	if err != nil {
		return err
	}
	trimmed := make([]string, len(urls))
	for i, u := range urls {
		trimmed[i] = strings.TrimSpace(u)
	}
	if err := controller.replace(nodesFromURLs(trimmed)); err != nil {
		return err
	}
	markOverridden(chainName, true)
	if m.kvstore != nil {
		if err := m.kvstore.SetAny(nodeOverridePrefix+chainName, trimmed); err != nil {
			return fmt.Errorf("nodes changed but not saved, so a restart reverts them: %w", err)
		}
	}
	logger.Info("RPC nodes changed by an operator", "chain", chainName, "count", len(trimmed))
	return nil
}

// ResetChainNodes goes back to the nodes in config.
func (m *Manager) ResetChainNodes(chainName string) error {
	controller, err := controllerFor(chainName)
	if err != nil {
		return err
	}
	if err := controller.replace(controller.configured); err != nil {
		return err
	}
	markOverridden(chainName, false)
	if m.kvstore != nil {
		_ = m.kvstore.Delete(nodeOverridePrefix + chainName)
	}
	logger.Info("RPC nodes reset to config", "chain", chainName)
	return nil
}

// QueueRescan hands a block range to the chain's manual worker (services.worker.manual must be enabled).
func (m *Manager) QueueRescan(ctx context.Context, chainName string, from, to uint64) error {
	internalCode, ok := m.internalCodes[chainName]
	if !ok {
		return ErrUnknownChain
	}
	if m.missingBlocks == nil {
		return fmt.Errorf("no Redis: re-scans need the manual worker's queue")
	}
	if from == 0 || to < from || to-from >= 100_000 {
		return fmt.Errorf("give a range of 1 to 100,000 blocks, from <= to")
	}
	return m.missingBlocks.AddMissingBlockRange(ctx, internalCode, from, to)
}

// ChainForNetwork finds the config key of the chain whose network_id is given (or the key itself).
func (m *Manager) ChainForNetwork(network string) (string, bool) {
	if _, ok := m.internalCodes[network]; ok {
		return network, true
	}
	chain, ok := m.networkChains[network]
	return chain, ok
}

func markOverridden(chainName string, overridden bool) {
	nodeControllersMu.Lock()
	defer nodeControllersMu.Unlock()
	if controller, ok := nodeControllers[chainName]; ok {
		controller.overridden = overridden
	}
}

func nodesOverridden(chainName string) bool {
	nodeControllersMu.RLock()
	defer nodeControllersMu.RUnlock()
	controller, ok := nodeControllers[chainName]
	return ok && controller.overridden
}

func controllerFor(chainName string) (*nodeController, error) {
	nodeControllersMu.RLock()
	defer nodeControllersMu.RUnlock()
	controller, ok := nodeControllers[chainName]
	if !ok {
		return nil, ErrUnknownChain
	}
	if controller.replace == nil {
		return nil, ErrNodeControlMissing
	}
	return controller, nil
}

func newMissingBlocksStore(redisClient infra.RedisClient) missingblockstore.MissingBlocksStore {
	if redisClient == nil || redisClient.GetClient() == nil {
		return nil
	}
	return missingblockstore.NewMissingBlocksStore(redisClient)
}
