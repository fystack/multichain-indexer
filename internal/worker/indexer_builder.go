package worker

import (
	"context"
	"strconv"

	"github.com/fystack/multichain-indexer/internal/indexer"
	tonIndexer "github.com/fystack/multichain-indexer/internal/indexer/ton"
	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/bitcoin"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/internal/rpc/sui"
	tonRpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/internal/rpc/tron"
	tonWorker "github.com/fystack/multichain-indexer/internal/worker/ton"
	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
	"gorm.io/gorm"
)

// ManagerConfig defines which workers to enable per chain.
type ManagerConfig struct {
	Chains        []string
	EnableRegular bool
	EnableCatchup bool
	EnableManual  bool
}

func sharedRateLimiter(chainName string, chainCfg config.ChainConfig) *ratelimiter.PooledRateLimiter {
	return ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName,
		chainCfg.Throttle.RPS,
		chainCfg.Throttle.Burst,
	)
}

func buildAuthConfig(node config.NodeConfig) *rpc.AuthConfig {
	return &rpc.AuthConfig{
		Type:  rpc.AuthType(node.Auth.Type),
		Key:   node.Auth.Key,
		Value: node.Auth.Value,
	}
}

func providerName(chainName string, index int) string {
	return chainName + "-" + strconv.Itoa(index+1)
}

func addProvider[T rpc.NetworkClient](
	failover *rpc.Failover[T],
	chainName string,
	nodeURL string,
	index int,
	clientType string,
	client rpc.NetworkClient,
) {
	failover.AddProvider(&rpc.Provider{
		Name:       providerName(chainName, index),
		URL:        nodeURL,
		Network:    chainName,
		ClientType: clientType,
		Client:     client,
		State:      rpc.StateHealthy,
	})
}

func buildEVMIndexer(chainName string, chainCfg config.ChainConfig, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[evm.EthereumAPI](nil)
	rl := sharedRateLimiter(chainName, chainCfg)
	for i, node := range chainCfg.Nodes {
		client := evm.NewEthereumClient(node.URL, buildAuthConfig(node), chainCfg.Client.Timeout, rl)
		addProvider(failover, chainName, node.URL, i, "rpc", client)
	}
	return indexer.NewEVMIndexer(chainName, chainCfg, failover, pubkeyStore)
}

func buildTronIndexer(chainName string, chainCfg config.ChainConfig, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[tron.TronAPI](nil)
	rl := sharedRateLimiter(chainName, chainCfg)
	for i, node := range chainCfg.Nodes {
		client := tron.NewTronClient(node.URL, buildAuthConfig(node), chainCfg.Client.Timeout, rl)
		addProvider(failover, chainName, node.URL, i, "rpc", client)
	}
	return indexer.NewTronIndexer(chainName, chainCfg, failover, pubkeyStore)
}

func buildBitcoinIndexer(chainName string, chainCfg config.ChainConfig, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[bitcoin.BitcoinAPI](nil)
	rl := sharedRateLimiter(chainName, chainCfg)
	for i, node := range chainCfg.Nodes {
		client := bitcoin.NewBitcoinClient(node.URL, buildAuthConfig(node), chainCfg.Client.Timeout, rl)
		addProvider(failover, chainName, node.URL, i, "rpc", client)
	}
	return indexer.NewBitcoinIndexer(chainName, chainCfg, failover, pubkeyStore)
}

func buildSolanaIndexer(chainName string, chainCfg config.ChainConfig, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[solana.SolanaAPI](nil)
	rl := sharedRateLimiter(chainName, chainCfg)
	for i, node := range chainCfg.Nodes {
		client := solana.NewSolanaClient(node.URL, buildAuthConfig(node), chainCfg.Client.Timeout, rl)
		addProvider(failover, chainName, node.URL, i, "rpc", client)
	}
	return indexer.NewSolanaIndexer(chainName, chainCfg, failover, pubkeyStore)
}

func buildSuiIndexer(chainName string, chainCfg config.ChainConfig, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[sui.SuiAPI](nil)
	for i, node := range chainCfg.Nodes {
		client := sui.NewSuiClient(node.URL)
		addProvider(failover, chainName, node.URL, i, "grpc", client)
	}
	return indexer.NewSuiIndexer(chainName, chainCfg, failover, pubkeyStore)
}

func buildIndexer(chainName string, chainCfg config.ChainConfig, pubkeyStore pubkeystore.Store) indexer.Indexer {
	switch chainCfg.Type {
	case enum.NetworkTypeEVM:
		return buildEVMIndexer(chainName, chainCfg, pubkeyStore)
	case enum.NetworkTypeTron:
		return buildTronIndexer(chainName, chainCfg, pubkeyStore)
	case enum.NetworkTypeBtc:
		return buildBitcoinIndexer(chainName, chainCfg, pubkeyStore)
	case enum.NetworkTypeSol:
		return buildSolanaIndexer(chainName, chainCfg, pubkeyStore)
	case enum.NetworkTypeSui:
		return buildSuiIndexer(chainName, chainCfg, pubkeyStore)
	default:
		return nil
	}
}

// buildTonPollingWorker constructs a TON polling worker with failover.
// TON uses account-based polling instead of block-based indexing.
func buildTonPollingWorker(
	ctx context.Context,
	chainName string,
	chainCfg config.ChainConfig,
	kvstore infra.KVStore,
	redisClient infra.RedisClient,
	db *gorm.DB,
	emitter events.Emitter,
) Worker {
	var client tonRpc.TonAPI
	if len(chainCfg.Nodes) > 0 {
		configURL := chainCfg.Nodes[0].URL
		var err error
		client, err = tonRpc.NewClient(ctx, tonRpc.ClientConfig{ConfigURL: configURL})
		if err != nil {
			logger.Error("Failed to create TON client with global config", "url", configURL, "err", err)
		}
	} else {
		logger.Error("No nodes configured for TON chain", "chain", chainName)
	}

	cursorStore := tonIndexer.NewCursorStore(kvstore)
	jettons := make([]tonIndexer.JettonInfo, 0, len(chainCfg.Jettons))
	for _, j := range chainCfg.Jettons {
		jettons = append(jettons, tonIndexer.JettonInfo{
			MasterAddress: j.MasterAddress,
			Symbol:        j.Symbol,
			Decimals:      j.Decimals,
		})
	}

	redisJettonRegistry := tonIndexer.NewRedisJettonRegistry(chainName, redisClient, jettons)
	if err := redisJettonRegistry.Reload(ctx); err != nil {
		logger.Error("Failed to load jetton registry from redis, using fallback config",
			"chain", chainName,
			"err", err,
		)
	}

	accountIndexer := tonIndexer.NewTonAccountIndexer(chainName, chainCfg, client, redisJettonRegistry)
	return tonWorker.NewTonPollingWorker(
		ctx,
		chainName,
		chainCfg,
		accountIndexer,
		cursorStore,
		db,
		kvstore,
		emitter,
		tonWorker.WorkerConfig{
			Concurrency:  chainCfg.Throttle.Concurrency,
			PollInterval: chainCfg.PollInterval,
		},
	)
}

// CreateManagerWithWorkers initializes manager and all workers for configured chains.
func CreateManagerWithWorkers(
	ctx context.Context,
	cfg *config.Config,
	kvstore infra.KVStore,
	db *gorm.DB,
	addressBF addressbloomfilter.WalletAddressBloomFilter,
	emitter events.Emitter,
	redisClient infra.RedisClient,
	managerCfg ManagerConfig,
) *Manager {
	blockStore := blockstore.NewBlockStore(kvstore)
	pubkeyStore := pubkeystore.NewPublicKeyStore(addressBF)
	failureQueue := NewRedisFailureQueue(redisClient)

	manager := NewManager(kvstore, blockStore, emitter, pubkeyStore)

	for _, chainName := range managerCfg.Chains {
		chainCfg, err := cfg.Chains.GetChain(chainName)
		if err != nil {
			logger.Error("Chain not found in config", "chain", chainName, "err", err)
			continue
		}

		if chainCfg.Type == enum.NetworkTypeTon {
			tonW := buildTonPollingWorker(ctx, chainName, chainCfg, kvstore, redisClient, db, emitter)
			if tonW != nil {
				manager.AddWorkers(tonW)
				logger.Info("Worker enabled", "chain", chainName, "mode", "ton_polling")
			}
			continue
		}

		idxr := buildIndexer(chainName, chainCfg, pubkeyStore)
		if idxr == nil {
			logger.Fatal("Unsupported network type", "chain", chainName, "type", chainCfg.Type)
		}

		deps := WorkerDeps{
			Chain:        idxr,
			Config:       chainCfg,
			BlockStore:   blockStore,
			PubkeyStore:  pubkeyStore,
			Emitter:      emitter,
			FailureQueue: failureQueue,
			Redis:        redisClient,
		}

		enableRegular := managerCfg.EnableRegular || cfg.Services.Worker.Regular.Enabled
		if enableRegular {
			manager.AddWorkers(NewRegularWorker(ctx, deps))
			logger.Info("Worker enabled", "chain", chainName, "mode", ModeRegular)
		} else {
			logger.Info("Worker disabled", "chain", chainName, "mode", ModeRegular)
		}

		enableCatchup := managerCfg.EnableCatchup || cfg.Services.Worker.Catchup.Enabled
		if enableCatchup {
			manager.AddWorkers(NewCatchupWorker(ctx, deps))
			logger.Info("Worker enabled", "chain", chainName, "mode", ModeCatchup)
		} else {
			logger.Info("Worker disabled", "chain", chainName, "mode", ModeCatchup)
		}

		enableManual := managerCfg.EnableManual || cfg.Services.Worker.Manual.Enabled
		if enableManual {
			manager.AddWorkers(NewManualWorker(ctx, deps))
			logger.Info("Worker enabled", "chain", chainName, "mode", ModeManual)
		} else {
			logger.Info("Worker disabled", "chain", chainName, "mode", ModeManual)
		}

		if chainCfg.Type == enum.NetworkTypeBtc && cfg.Services.Worker.Mempool.Enabled {
			logger.Warn("Legacy mempool worker is isolated behind build tag",
				"chain", chainName,
				"legacy_build_tag", "legacyworkers",
				"active_modes", "regular/catchup/manual",
			)
		}
	}

	return manager
}
