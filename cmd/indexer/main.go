package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"gorm.io/gorm"

	"github.com/fystack/multichain-indexer/internal/worker"
	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/kvstore"
)

type CLI struct {
	Index IndexCmd `cmd:"" help:"Start the multi-chain transaction indexer with configurable worker modes."`
}

type IndexCmd struct {
	// Core configuration
	ConfigPath string `help:"Path to configuration file containing chain and worker settings." default:"configs/config.yaml" short:"c" name:"config"`

	// Chain selection
	Chains []string `help:"Specific blockchain chains to index (comma-separated). If not specified, all configured chains will be indexed." sep:"," short:"n" name:"chains" placeholder:"ethereum,polygon"`

	// Logging
	Debug bool `help:"Enable debug-level logging for detailed troubleshooting information." short:"d" name:"debug"`

	// Worker modes (these can override config settings)
	EnableCatchup bool `help:"Enable catchup worker to process historical blocks alongside regular indexing. Overrides config setting." name:"catchup"`
	EnableManual  bool `help:"Enable manual worker for processing specific block ranges via Redis queue. Overrides config setting."     name:"manual"`

	// Block starting point
	FromLatest bool `help:"Start indexing from the latest blockchain block instead of configured starting points. Useful for fresh deployments." name:"from-latest"`
}

func (c *IndexCmd) Run() error {
	runIndexer(c.Chains, c.ConfigPath, c.Debug, c.EnableManual, c.EnableCatchup, c.FromLatest)
	return nil
}

func main() {
	var cli CLI
	ctx := kong.Parse(
		&cli,
		kong.Name("transaction-indexer"),
		kong.Description(
			"Multi-chain blockchain transaction indexer with support for regular, catchup, manual, and rescanner worker modes.",
		),
		kong.UsageOnError(),
		kong.Vars{
			"version": "1.0.0", // You can add version info
		},
	)
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}

func runIndexer(chains []string, configPath string, debug, manual, catchup, fromLatest bool) {
	ctx := context.Background()

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logger.Init(&logger.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Fatal("Failed to load configuration",
			"config_path", configPath,
			"error", err.Error(),
			"hint", "Check the config file syntax and structure")
	}
	logger.Info("Config loaded", "environment", cfg.Environment)

	services := cfg.Services
	// start redis
	logger.Info(
		"Connecting to redis",
		"url",
		services.Redis.URL,
		"environment",
		cfg.Environment,
		"mtls",
		services.Redis.MTLS,
	)
	redisClient, err := infra.NewRedisClient(
		services.Redis.URL,
		services.Redis.Password,
		string(cfg.Environment),
		services.Redis.MTLS,
	)
	if err != nil {
		logger.Fatal("Create redis client failed", "err", err)
	}
	logger.Info("Redis connection established")

	// start db (optional)
	var db *gorm.DB
	if services.Database != nil {
		db, err = infra.NewDBConnection(services.Database.URL, string(cfg.Environment))
		if err != nil {
			logger.Fatal("Create db connection failed", "err", err)
		}
		logger.Info("Database connection established")
	} else {
		logger.Info("Database connection skipped - not configured")
	}

	// start kvstore
	logger.Info("Connecting to kvstore", "url", services.KVS.Consul.Address)
	kvstore, err := kvstore.NewFromConfig(services.KVS)
	if err != nil {
		logger.Fatal("Create kvstore failed", "err", err)
	}
	defer kvstore.Close()

	logger.Info("Connecting to NATS", "url", services.Nats.URL)
	natsConn, err := infra.GetNATSConnection(services.Nats, string(cfg.Environment))
	if err != nil {
		logger.Fatal("Create NATS connection failed", "err", err)
	}
	defer natsConn.Close()
	logger.Info("NATS connection established")

	transferEventQueueManager := infra.NewNATsMessageQueueManager("transfer", []string{
		"transfer.event.*",
	}, natsConn)

	transferQueue := transferEventQueueManager.NewMessageQueue("dispatch")

	emitter := events.NewEmitter(transferQueue, services.Nats.SubjectPrefix)
	defer emitter.Close()

	// start address bloom filter (Initialize is optional)
	var addressBF addressbloomfilter.WalletAddressBloomFilter
	if services.Bloomfilter != nil && db != nil {
		addressBF = addressbloomfilter.NewBloomFilter(*services.Bloomfilter, db, redisClient)
		if err := addressBF.Initialize(ctx); err != nil {
			logger.Fatal("Address bloom filter init failed", "err", err)
		}
		logger.Info("Address bloom filter initialized")
	} else if services.Bloomfilter != nil {
		// Create bloom filter instance even without database, but skip initialization
		addressBF = addressbloomfilter.NewBloomFilter(*services.Bloomfilter, db, redisClient)
		logger.Info("Address bloom filter created (initialization skipped - no database)")
	} else {
		logger.Info("Address bloom filter skipped - not configured")
	}

	// If no chains specified, use all configured chains
	if len(chains) == 0 {
		chains = cfg.Chains.Names()
		logger.Info("No chains specified, using all configured chains", "chains", chains)
	} else {
		logger.Info("Indexing specified chains", "chains", chains)
	}

	// Validate chains
	if err := cfg.Chains.Validate(chains); err != nil {
		logger.Fatal("Validate chains failed", "err", err)
	}

	// Override from_latest from CLI if requested
	if fromLatest {
		cfg.Chains.OverrideFromLatest(chains)
		logger.Info("Starting from latest block for all specified chains", "chains", chains)
	}

	// Create manager with all workers using factory
	managerCfg := worker.ManagerConfig{
		Chains:        chains,
		EnableCatchup: catchup,
		EnableManual:  manual,
	}

	manager := worker.CreateManagerWithWorkers(
		ctx,
		cfg,
		kvstore,
		db,
		addressBF,
		emitter,
		redisClient,
		managerCfg,
	)
	tonJettonReloadService := worker.NewTonJettonReloadServiceFromManager(manager)

	healthServer := startHealthServer(cfg.Services.Port, cfg, tonJettonReloadService)

	// Start all workers
	logger.Info("Starting all workers")
	manager.Start()

	logger.Info("🚀 Transaction indexer is running... Press Ctrl+C to stop")
	waitForShutdown()

	logger.Info("Shutting down indexer...")

	// Shutdown health server
	if healthServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := healthServer.Shutdown(ctx); err != nil {
			logger.Error("Health server shutdown failed", "error", err)
		} else {
			logger.Info("Health server stopped gracefully")
		}
	}

	manager.Stop()
	logger.Info("✅ Indexer stopped gracefully")
}

type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version"`
}

type APIErrorResponse struct {
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}

type TonJettonReloadResponse struct {
	Status         string                         `json:"status"`
	Chain          string                         `json:"chain,omitempty"`
	Results        []worker.TonJettonReloadResult `json:"results"`
	TriggeredAtUTC time.Time                      `json:"triggered_at_utc"`
}

func startHealthServer(
	port int,
	cfg *config.Config,
	tonJettonReloadService *worker.TonJettonReloadService,
) *http.Server {
	mux := http.NewServeMux()

	version := cfg.Version
	if version == "" {
		version = "1.0.0" // fallback version
	}

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		response := HealthResponse{
			Status:    "ok",
			Timestamp: time.Now().UTC(),
			Version:   version,
		}
		writeJSON(w, http.StatusOK, response)
	})

	mux.HandleFunc("/ton/jettons/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if tonJettonReloadService == nil {
			writeErrorJSON(w, http.StatusNotFound, worker.ErrNoTonWorkerConfigured.Error())
			return
		}

		req := worker.TonJettonReloadRequest{
			ChainFilter: r.URL.Query().Get("chain"),
		}

		results, err := tonJettonReloadService.ReloadTonJettons(r.Context(), req)
		if err != nil {
			statusCode := http.StatusInternalServerError
			if errors.Is(err, worker.ErrTonWorkerNotFound) || errors.Is(err, worker.ErrNoTonWorkerConfigured) {
				statusCode = http.StatusNotFound
			}
			writeErrorJSON(w, statusCode, err.Error())
			return
		}

		response := TonJettonReloadResponse{
			Status:         reloadJettonStatus(results),
			Chain:          req.ChainFilter,
			Results:        results,
			TriggeredAtUTC: time.Now().UTC(),
		}
		writeJSON(w, http.StatusOK, response)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		logger.Info(
			"Health check server started",
			"port", port,
			"health_endpoint", "/health",
			"jetton_reload_endpoint", "/ton/jettons/reload",
		)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Health server failed to start", "error", err)
		}
	}()

	return server
}

func reloadJettonStatus(results []worker.TonJettonReloadResult) string {
	for _, result := range results {
		if result.Error != "" {
			return "partial_error"
		}
	}
	return "ok"
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.Error("Failed to encode response", "status", statusCode, "err", err)
	}
}

func writeErrorJSON(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, APIErrorResponse{
		Status:    "error",
		Error:     message,
		Timestamp: time.Now().UTC(),
	})
}

func waitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
