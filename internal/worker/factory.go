package worker

import (
	"context"
	"strconv"

	"github.com/fystack/multichain-indexer/internal/indexer"
	tonIndexer "github.com/fystack/multichain-indexer/internal/indexer/ton"
	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/bitcoin"
	"github.com/fystack/multichain-indexer/internal/rpc/cosmos"
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

// WorkerDeps bundles dependencies injected into workers.
type WorkerDeps struct {
	Ctx        context.Context
	KVStore    infra.KVStore
	BlockStore blockstore.Store
	Emitter    events.Emitter
	Pubkey     pubkeystore.Store
	Redis      infra.RedisClient
	FailedChan chan FailedBlockEvent
}

// ManagerConfig defines which workers to enable per chain.
type ManagerConfig struct {
	Chains          []string
	EnableRegular   bool
	EnableRescanner bool
	EnableCatchup   bool
	EnableManual    bool
}

// BuildWorkers constructs workers for a given mode.
// Note: This function now expects the indexer to be created with the appropriate mode-scoped rate limiter
func BuildWorkers(
	idxr indexer.Indexer,
	cfg config.ChainConfig,
	mode WorkerMode,
	deps WorkerDeps,
) []Worker {
	switch mode {
	case ModeRegular:
		return []Worker{
			NewRegularWorker(
				deps.Ctx,
				idxr,
				cfg,
				deps.KVStore,
				deps.BlockStore,
				deps.Emitter,
				deps.Pubkey,
				deps.FailedChan,
			),
		}
	case ModeCatchup:
		return []Worker{
			NewCatchupWorker(
				deps.Ctx,
				idxr,
				cfg,
				deps.KVStore,
				deps.BlockStore,
				deps.Emitter,
				deps.Pubkey,
				deps.FailedChan,
			),
		}
	case ModeRescanner:
		return []Worker{
			NewRescannerWorker(
				deps.Ctx,
				idxr,
				cfg,
				deps.KVStore,
				deps.BlockStore,
				deps.Emitter,
				deps.Pubkey,
				deps.FailedChan,
			),
		}
	case ModeManual:
		return []Worker{
			NewManualWorker(
				deps.Ctx,
				idxr,
				cfg,
				deps.KVStore,
				deps.Redis,
				deps.BlockStore,
				deps.Emitter,
				deps.Pubkey,
				deps.FailedChan,
			),
		}
	case ModeMempool:
		return []Worker{
			NewMempoolWorker(
				deps.Ctx,
				idxr,
				cfg,
				deps.KVStore,
				deps.BlockStore,
				deps.Emitter,
				deps.Pubkey,
				deps.FailedChan,
			),
		}
	default:
		return nil
	}
}

// buildEVMIndexer constructs an EVM indexer with failover and providers.
func buildEVMIndexer(chainName string, chainCfg config.ChainConfig, mode WorkerMode, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[evm.EthereumAPI](nil)

	// Shared rate limiter for all workers of this chain (global across regular, catchup, etc.)
	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	for i, node := range chainCfg.Nodes {
		client := evm.NewEthereumClient(
			node.URL,
			&rpc.AuthConfig{
				Type:  rpc.AuthType(node.Auth.Type),
				Key:   node.Auth.Key,
				Value: node.Auth.Value,
			},
			chainCfg.Client.Timeout,
			rl,
		)

		failover.AddProvider(&rpc.Provider{
			Name:       chainName + "-" + strconv.Itoa(i+1),
			URL:        node.URL,
			Network:    chainName,
			ClientType: "rpc",
			Client:     client,
			State:      rpc.StateHealthy, // Initialize as healthy
		})
	}

	return indexer.NewEVMIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// buildTronIndexer constructs a Tron indexer with failover and providers.
func buildTronIndexer(chainName string, chainCfg config.ChainConfig, mode WorkerMode, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[tron.TronAPI](nil)

	// Shared rate limiter for all workers of this chain (global across regular, catchup, etc.)
	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	for i, node := range chainCfg.Nodes {
		client := tron.NewTronClient(
			node.URL,
			&rpc.AuthConfig{
				Type:  rpc.AuthType(node.Auth.Type),
				Key:   node.Auth.Key,
				Value: node.Auth.Value,
			},
			chainCfg.Client.Timeout,
			rl,
		)

		failover.AddProvider(&rpc.Provider{
			Name:       chainName + "-" + strconv.Itoa(i+1),
			URL:        node.URL,
			Network:    chainName,
			ClientType: "rpc",
			Client:     client,
			State:      rpc.StateHealthy, // Initialize as healthy
		})
	}

	return indexer.NewTronIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// buildBitcoinIndexer constructs a Bitcoin indexer with failover and providers.
func buildBitcoinIndexer(
	chainName string,
	chainCfg config.ChainConfig,
	mode WorkerMode,
	pubkeyStore pubkeystore.Store,
) indexer.Indexer {
	failover := rpc.NewFailover[bitcoin.BitcoinAPI](nil)

	// Shared rate limiter for all workers of this chain (global across regular, catchup, etc.)
	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	for i, node := range chainCfg.Nodes {
		client := bitcoin.NewBitcoinClient(
			node.URL,
			&rpc.AuthConfig{
				Type:  rpc.AuthType(node.Auth.Type),
				Key:   node.Auth.Key,
				Value: node.Auth.Value,
			},
			chainCfg.Client.Timeout,
			rl,
		)

		failover.AddProvider(&rpc.Provider{
			Name:       chainName + "-" + strconv.Itoa(i+1),
			URL:        node.URL,
			Network:    chainName,
			ClientType: "rpc",
			Client:     client,
			State:      rpc.StateHealthy, // Initialize as healthy
		})
	}

	return indexer.NewBitcoinIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// buildSolanaIndexer constructs a Solana indexer with failover and providers.
func buildSolanaIndexer(chainName string, chainCfg config.ChainConfig, mode WorkerMode, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[solana.SolanaAPI](nil)

	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	for i, node := range chainCfg.Nodes {
		client := solana.NewSolanaClient(
			node.URL,
			&rpc.AuthConfig{
				Type:  rpc.AuthType(node.Auth.Type),
				Key:   node.Auth.Key,
				Value: node.Auth.Value,
			},
			chainCfg.Client.Timeout,
			rl,
		)

		failover.AddProvider(&rpc.Provider{
			Name:       chainName + "-" + strconv.Itoa(i+1),
			URL:        node.URL,
			Network:    chainName,
			ClientType: "rpc",
			Client:     client,
			State:      rpc.StateHealthy,
		})
	}

	return indexer.NewSolanaIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// buildSuiIndexer constructs a Sui indexer with failover and providers.
func buildSuiIndexer(
	chainName string,
	chainCfg config.ChainConfig,
	mode WorkerMode,
	pubkeyStore pubkeystore.Store,
) indexer.Indexer {
	failover := rpc.NewFailover[sui.SuiAPI](nil)

	for i, node := range chainCfg.Nodes {
		client := sui.NewSuiClient(node.URL)

		failover.AddProvider(&rpc.Provider{
			Name:       chainName + "-" + strconv.Itoa(i+1),
			URL:        node.URL,
			Network:    chainName,
			ClientType: "grpc",
			Client:     client,
			State:      rpc.StateHealthy,
		})
	}

	return indexer.NewSuiIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// buildCosmosIndexer constructs a Cosmos indexer with failover and providers.
func buildCosmosIndexer(
	chainName string,
	chainCfg config.ChainConfig,
	mode WorkerMode,
	pubkeyStore pubkeystore.Store,
) indexer.Indexer {
	failover := rpc.NewFailover[cosmos.CosmosAPI](nil)

	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	for i, node := range chainCfg.Nodes {
		client := cosmos.NewCosmosClient(
			node.URL,
			&rpc.AuthConfig{
				Type:  rpc.AuthType(node.Auth.Type),
				Key:   node.Auth.Key,
				Value: node.Auth.Value,
			},
			chainCfg.Client.Timeout,
			rl,
		)

		failover.AddProvider(&rpc.Provider{
			Name:       chainName + "-" + strconv.Itoa(i+1),
			URL:        node.URL,
			Network:    chainName,
			ClientType: rpc.ClientTypeREST,
			Client:     client,
			State:      rpc.StateHealthy,
		})
	}

	return indexer.NewCosmosIndexer(chainName, chainCfg, failover, pubkeyStore)
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

		var err error // shadow assignment
		client, err = tonRpc.NewClient(ctx, tonRpc.ClientConfig{
			ConfigURL: configURL,
		})

		if err != nil {
			logger.Error("Failed to create TON client with global config", "url", configURL, "err", err)
			// Proceed with nil client, IsHealthy() will return false
		}
	} else {
		logger.Error("No nodes configured for TON chain", "chain", chainName)
	}

	// Create cursor store backed by KVStore
	cursorStore := tonIndexer.NewCursorStore(kvstore)

	// Create Jetton registry
	var jettons []tonIndexer.JettonInfo
	for _, j := range chainCfg.Jettons {
		jettons = append(jettons, tonIndexer.JettonInfo{
			MasterAddress: j.MasterAddress,
			Symbol:        j.Symbol,
			Decimals:      j.Decimals,
		})
	}
	jettonRegistry := tonIndexer.NewRedisJettonRegistry(chainName, redisClient, jettons)
	if err := jettonRegistry.Reload(ctx); err != nil {
		logger.Error("Failed to load jetton registry from redis, using fallback config",
			"chain", chainName,
			"err", err,
		)
	}

	// Create the account indexer
	accountIndexer := tonIndexer.NewTonAccountIndexer(
		chainName,
		chainCfg,
		client,
		jettonRegistry,
	)

	// Create the polling worker
	worker := tonWorker.NewTonPollingWorker(
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
	return worker
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
	// Shared stores
	blockStore := blockstore.NewBlockStore(kvstore)
	pubkeyStore := pubkeystore.NewPublicKeyStore(addressBF)
	failedChan := make(chan FailedBlockEvent, 100)

	manager := NewManager(ctx, kvstore, blockStore, emitter, pubkeyStore, failedChan)

	// Loop each chain
	for _, chainName := range managerCfg.Chains {
		chainCfg, err := cfg.Chains.GetChain(chainName)
		if err != nil {
			logger.Error("Chain not found in config", "chain", chainName, "err", err)
			continue
		}

		// Build indexer once - shared across all worker modes with global rate limiter
		var idxr indexer.Indexer
		switch chainCfg.Type {
		case enum.NetworkTypeEVM:
			idxr = buildEVMIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeTron:
			idxr = buildTronIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeBtc:
			idxr = buildBitcoinIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeSol:
			idxr = buildSolanaIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeSui:
			idxr = buildSuiIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeCosmos:
			idxr = buildCosmosIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeTon:
			tonW := buildTonPollingWorker(ctx, chainName, chainCfg, kvstore, redisClient, db, emitter)
			if tonW != nil {
				manager.AddWorkers(tonW)
				logger.Info("TON polling worker enabled", "chain", chainName)
			}
			continue
		default:
			logger.Fatal("Unsupported network type", "chain", chainName, "type", chainCfg.Type)
		}

		// Worker deps
		deps := WorkerDeps{
			Ctx:        ctx,
			KVStore:    kvstore,
			BlockStore: blockStore,
			Emitter:    emitter,
			Pubkey:     pubkeyStore,
			Redis:      redisClient,
			FailedChan: failedChan,
		}

		// Helper: add workers if enabled (all modes share the same indexer and global rate limiter)
		addIfEnabled := func(mode WorkerMode, enabled bool) {
			if enabled {
				ws := BuildWorkers(idxr, chainCfg, mode, deps)
				manager.AddWorkers(ws...)
				logger.Info("Worker enabled", "chain", chainName, "mode", mode)
			} else {
				logger.Info("Worker disabled", "chain", chainName, "mode", mode)
			}
		}

		addIfEnabled(ModeRegular, managerCfg.EnableRegular || cfg.Services.Worker.Regular.Enabled)
		addIfEnabled(
			ModeRescanner,
			managerCfg.EnableRescanner || cfg.Services.Worker.Rescanner.Enabled,
		)
		addIfEnabled(ModeCatchup, managerCfg.EnableCatchup || cfg.Services.Worker.Catchup.Enabled)
		addIfEnabled(ModeManual, managerCfg.EnableManual || cfg.Services.Worker.Manual.Enabled)

		// Mempool worker is Bitcoin-specific (0-conf transaction tracking)
		if chainCfg.Type == enum.NetworkTypeBtc {
			addIfEnabled(ModeMempool, cfg.Services.Worker.Mempool.Enabled)
		}
	}

	return manager
}
