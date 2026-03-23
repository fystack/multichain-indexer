package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/aptos"
	"github.com/fystack/multichain-indexer/internal/rpc/bitcoin"
	"github.com/fystack/multichain-indexer/internal/rpc/cosmos"
	"github.com/fystack/multichain-indexer/internal/rpc/evm"
	"github.com/fystack/multichain-indexer/internal/rpc/solana"
	"github.com/fystack/multichain-indexer/internal/rpc/sui"
	tonrpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/internal/rpc/tron"
	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/ratelimiter"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
	tonaddr "github.com/xssnick/tonutils-go/address"
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
	Observer   BlockResultObserver
}

// ManagerConfig defines which workers to enable per chain.
type ManagerConfig struct {
	Chains          []string
	EnableRegular   bool
	EnableRescanner bool
	EnableCatchup   bool
	EnableManual    bool
	Observer        BlockResultObserver
	BloomSync       *BloomSyncConfig // nil = disabled
}

// setObserverOnWorkers injects the observer callback into each worker's BaseWorker.
func setObserverOnWorkers(workers []Worker, obs BlockResultObserver) {
	if obs == nil {
		return
	}
	for _, w := range workers {
		switch wt := w.(type) {
		case *RegularWorker:
			wt.BaseWorker.observer = obs
		case *CatchupWorker:
			wt.BaseWorker.observer = obs
		case *RescannerWorker:
			wt.BaseWorker.observer = obs
		case *ManualWorker:
			wt.BaseWorker.observer = obs
		case *MempoolWorker:
			wt.BaseWorker.observer = obs
		}
	}
}

const (
	tonAssetCachePrefix                = "assetcache"
	tonPreloadJettonResolveTimeout     = 6 * time.Second
	tonPreloadJettonConcurrencyDefault = 8
)

// BuildWorkers constructs workers for a given mode.
// Note: This function now expects the indexer to be created with the appropriate mode-scoped rate limiter
func BuildWorkers(
	idxr indexer.Indexer,
	cfg config.ChainConfig,
	mode WorkerMode,
	deps WorkerDeps,
) []Worker {
	var workers []Worker

	switch mode {
	case ModeRegular:
		workers = []Worker{
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
		workers = []Worker{
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
		workers = []Worker{
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
		workers = []Worker{
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
		workers = []Worker{
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

	setObserverOnWorkers(workers, deps.Observer)
	return workers
}

func newEVMProvider(chainName string, idx int, node config.NodeConfig, timeout time.Duration,
	rl *ratelimiter.PooledRateLimiter) *rpc.Provider {
	client := evm.NewEthereumClient(
		node.URL,
		&rpc.AuthConfig{Type: rpc.AuthType(node.Auth.Type), Key: node.Auth.Key, Value: node.Auth.Value},
		timeout, rl,
	)
	if len(node.Headers) > 0 {
		client.SetCustomHeaders(node.Headers)
	}
	return &rpc.Provider{
		Name:       chainName + "-" + strconv.Itoa(idx),
		URL:        node.URL,
		Network:    chainName,
		ClientType: "rpc",
		Client:     client,
		State:      rpc.StateHealthy,
	}
}

// buildEVMIndexer constructs an EVM indexer with failover and providers.
func buildEVMIndexer(chainName string, chainCfg config.ChainConfig, mode WorkerMode, pubkeyStore pubkeystore.Store) indexer.Indexer {
	failover := rpc.NewFailover[evm.EthereumAPI](nil)
	var traceFailover *rpc.Failover[evm.EthereumAPI]

	// Main pool rate limiter
	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	// Trace pool rate limiter — only created when debug_trace is enabled.
	// Dedicated budget to avoid starving main pool.
	// Defaults to half the main pool if not explicitly configured.
	var traceRL *ratelimiter.PooledRateLimiter
	if chainCfg.DebugTrace {
		traceRPS := chainCfg.TraceThrottle.RPS
		traceBurst := chainCfg.TraceThrottle.Burst
		if traceRPS <= 0 {
			traceRPS = max(1, chainCfg.Throttle.RPS/2)
		}
		if traceBurst <= 0 {
			traceBurst = max(1, chainCfg.Throttle.Burst/2)
		}
		// Note: keyed by chainName (not node URL), so all trace providers for this chain
		// share one budget. This is intentional — trace_rps/trace_burst is a chain-level
		// cap, not per-node. Same pattern as the main rate limiter.
		traceRL = ratelimiter.GetOrCreateScopedPooledRateLimiter(chainName, "trace", traceRPS, traceBurst)
	}

	for i, node := range chainCfg.Nodes {
		// Main pool provider
		failover.AddProvider(newEVMProvider(chainName, i+1, node, chainCfg.Client.Timeout, rl))

		// Trace pool: SEPARATE provider instance — no shared mutable state
		if node.DebugTrace && chainCfg.DebugTrace {
			if traceFailover == nil {
				traceFailover = rpc.NewFailover[evm.EthereumAPI](nil)
			}
			traceFailover.AddProvider(newEVMProvider(chainName+"-trace", i+1, node, chainCfg.Client.Timeout, traceRL))
		}
	}

	if chainCfg.DebugTrace && traceFailover == nil {
		logger.Warn("debug_trace enabled but no nodes have debug_trace: true", "chain", chainName)
	}

	return indexer.NewEVMIndexer(chainName, chainCfg, failover, traceFailover, pubkeyStore)
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

// buildAptosIndexer constructs an Aptos indexer with failover and providers.
func buildAptosIndexer(
	chainName string,
	chainCfg config.ChainConfig,
	mode WorkerMode,
	pubkeyStore pubkeystore.Store,
) indexer.Indexer {
	failover := rpc.NewFailover[aptos.AptosAPI](nil)

	rl := ratelimiter.GetOrCreateSharedPooledRateLimiter(
		chainName, chainCfg.Throttle.RPS, chainCfg.Throttle.Burst,
	)

	for i, node := range chainCfg.Nodes {
		client := aptos.NewAptosClient(
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

	return indexer.NewAptosIndexer(chainName, chainCfg, failover, pubkeyStore)
}

// buildTonIndexer constructs a TON indexer using tonutils lite-server client.
func buildTonIndexer(
	chainName string,
	chainCfg config.ChainConfig,
	pubkeyStore pubkeystore.Store,
	db *gorm.DB,
	redisClient infra.RedisClient,
) indexer.Indexer {
	if len(chainCfg.Nodes) == 0 {
		logger.Fatal("TON chain has no configured nodes", "chain", chainName)
	}

	client, err := tonrpc.NewClient(context.Background(), tonrpc.ClientConfig{
		ConfigURL: chainCfg.Nodes[0].URL,
	})
	if err != nil {
		logger.Fatal("Failed to initialize TON client", "chain", chainName, "err", err)
	}

	pretrackedJettonWallets := preloadTONJettonWallets(
		context.Background(),
		chainName,
		chainCfg,
		client,
		db,
		redisClient,
	)

	return indexer.NewTonIndexer(
		chainName,
		chainCfg,
		client,
		pubkeyStore,
		pretrackedJettonWallets,
	)
}

func preloadTONJettonWallets(
	ctx context.Context,
	chainName string,
	chainCfg config.ChainConfig,
	client tonrpc.TonAPI,
	db *gorm.DB,
	redisClient infra.RedisClient,
) []string {
	if db == nil {
		logger.Warn("skip TON jetton wallet preload: database not configured", "chain", chainName)
		return nil
	}
	if redisClient == nil || redisClient.GetClient() == nil {
		logger.Warn("skip TON jetton wallet preload: redis not configured", "chain", chainName)
		return nil
	}
	if client == nil {
		logger.Warn("skip TON jetton wallet preload: ton client not configured", "chain", chainName)
		return nil
	}

	owners, err := loadTONOwnerWallets(ctx, db)
	if err != nil {
		logger.Warn("failed loading TON owner wallets for jetton preload", "chain", chainName, "err", err)
		return nil
	}
	if len(owners) == 0 {
		logger.Info("skip TON jetton wallet preload: no TON owner wallets found", "chain", chainName)
		return nil
	}

	masters, err := loadTONJettonMastersFromCache(ctx, redisClient, chainName, chainCfg)
	if err != nil {
		logger.Warn("failed loading TON jetton masters from cache", "chain", chainName, "err", err)
		return nil
	}
	if len(masters) == 0 {
		logger.Info("skip TON jetton wallet preload: no TON jetton masters found in cache", "chain", chainName)
		return nil
	}

	pairCount := len(owners) * len(masters)
	logger.Info(
		"Preloading TON jetton wallets",
		"chain", chainName,
		"owners", len(owners),
		"jetton_masters", len(masters),
		"pairs", pairCount,
	)

	wallets := deriveTONJettonWallets(ctx, chainName, chainCfg, client, owners, masters)
	logger.Info(
		"TON jetton wallet preload completed",
		"chain", chainName,
		"owners", len(owners),
		"jetton_masters", len(masters),
		"jetton_wallets", len(wallets),
	)

	return wallets
}

func loadTONOwnerWallets(ctx context.Context, db *gorm.DB) ([]string, error) {
	repo := repository.NewRepository[model.WalletAddress](db)
	rows, err := repo.Find(ctx, repository.FindOptions{
		Where:  repository.WhereType{"type": enum.NetworkTypeTon},
		Select: []string{"address"},
	})
	if err != nil {
		return nil, err
	}

	set := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		normalized := normalizeTONAddressForPreload(row.Address)
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}

	owners := make([]string, 0, len(set))
	for owner := range set {
		owners = append(owners, owner)
	}
	return owners, nil
}

func loadTONJettonMastersFromCache(
	ctx context.Context,
	redisClient infra.RedisClient,
	chainName string,
	chainCfg config.ChainConfig,
) ([]string, error) {
	client := redisClient.GetClient()
	networkCodes := tonAssetCacheNetworkCodes(chainName, chainCfg)
	masterSet := make(map[string]struct{})

	for _, networkCode := range networkCodes {
		if networkCode == "" {
			continue
		}

		pattern := fmt.Sprintf("%s:%s:*", tonAssetCachePrefix, networkCode)
		prefix := fmt.Sprintf("%s:%s:", tonAssetCachePrefix, networkCode)
		var cursor uint64

		for {
			keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 500).Result()
			if err != nil {
				return nil, err
			}

			for _, key := range keys {
				contractAddress := strings.TrimSpace(strings.TrimPrefix(key, prefix))
				if contractAddress == "" || strings.EqualFold(contractAddress, "native") {
					continue
				}

				normalized := normalizeTONAddressForPreload(contractAddress)
				if normalized == "" {
					continue
				}
				masterSet[normalized] = struct{}{}
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
	}

	masters := make([]string, 0, len(masterSet))
	for master := range masterSet {
		masters = append(masters, master)
	}

	return masters, nil
}

func deriveTONJettonWallets(
	ctx context.Context,
	chainName string,
	chainCfg config.ChainConfig,
	client tonrpc.TonAPI,
	owners []string,
	masters []string,
) []string {
	if len(owners) == 0 || len(masters) == 0 {
		return nil
	}

	workerCount := chainCfg.Throttle.Concurrency
	if workerCount <= 0 {
		workerCount = tonPreloadJettonConcurrencyDefault
	}
	if workerCount > 32 {
		workerCount = 32
	}

	type resolveJob struct {
		owner  string
		master string
	}

	jobs := make(chan resolveJob, workerCount*2)
	wallets := make(chan string, workerCount*2)

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()

		for job := range jobs {
			callCtx, cancel := context.WithTimeout(ctx, tonPreloadJettonResolveTimeout)
			wallet, err := client.ResolveJettonWalletAddress(callCtx, job.master, job.owner)
			cancel()
			if err != nil {
				continue
			}

			normalized := normalizeTONAddressForPreload(wallet)
			if normalized == "" {
				continue
			}
			wallets <- normalized
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}

	go func() {
		defer close(jobs)
		for _, owner := range owners {
			for _, master := range masters {
				select {
				case <-ctx.Done():
					return
				case jobs <- resolveJob{
					owner:  owner,
					master: master,
				}:
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(wallets)
	}()

	walletSet := make(map[string]struct{})
	for wallet := range wallets {
		walletSet[wallet] = struct{}{}
	}

	out := make([]string, 0, len(walletSet))
	for wallet := range walletSet {
		out = append(out, wallet)
	}

	return out
}

func tonAssetCacheNetworkCodes(chainName string, chainCfg config.ChainConfig) []string {
	candidates := []string{
		chainCfg.InternalCode,
		strings.ToUpper(chainCfg.InternalCode),
		strings.ToLower(chainCfg.InternalCode),
		chainCfg.NetworkId,
		strings.ToUpper(chainCfg.NetworkId),
		strings.ToLower(chainCfg.NetworkId),
		chainName,
		strings.ToUpper(chainName),
		strings.ToLower(chainName),
	}

	set := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		normalized := strings.TrimSpace(candidate)
		if normalized == "" {
			continue
		}
		if _, exists := set[normalized]; exists {
			continue
		}
		set[normalized] = struct{}{}
		out = append(out, normalized)
	}

	return out
}

func normalizeTONAddressForPreload(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}

	if parsed, err := tonaddr.ParseRawAddr(addr); err == nil && parsed != nil {
		return parsed.StringRaw()
	}
	if parsed, err := tonaddr.ParseAddr(addr); err == nil && parsed != nil {
		return parsed.StringRaw()
	}

	return ""
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

	manager := NewManager(ctx, kvstore, blockStore, emitter, pubkeyStore)

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
		case enum.NetworkTypeApt:
			idxr = buildAptosIndexer(chainName, chainCfg, ModeRegular, pubkeyStore)
		case enum.NetworkTypeTon:
			idxr = buildTonIndexer(chainName, chainCfg, pubkeyStore, db, redisClient)
		default:
			logger.Fatal("Unsupported network type", "chain", chainName, "type", chainCfg.Type)
		}

		failedChan := make(chan FailedBlockEvent, 100)

		// Worker deps
		deps := WorkerDeps{
			Ctx:        ctx,
			KVStore:    kvstore,
			BlockStore: blockStore,
			Emitter:    emitter,
			Pubkey:     pubkeyStore,
			Redis:      redisClient,
			FailedChan: failedChan,
			Observer:   managerCfg.Observer,
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

	// Bloom filter sync worker (global, not per-chain)
	if managerCfg.BloomSync != nil && db != nil && addressBF != nil {
		bloomWorker := NewBloomSyncWorker(ctx, addressBF, NewDefaultDBLoader(db), *managerCfg.BloomSync)
		manager.AddWorkers(bloomWorker)
		logger.Info("Bloom filter sync worker enabled",
			"interval", managerCfg.BloomSync.Interval,
			"batchSize", managerCfg.BloomSync.BatchSize,
		)
	}

	return manager
}
