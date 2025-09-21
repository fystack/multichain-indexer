package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"github.com/fystack/transaction-indexer/internal/worker"
	"github.com/fystack/transaction-indexer/pkg/addressbloomfilter"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/events"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
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
	redisClient, err := infra.NewRedisClient(
		services.Redis.URL,
		services.Redis.Password,
		string(cfg.Environment),
	)
	if err != nil {
		logger.Fatal("Create redis client failed", "err", err)
	}

	// start db
	db, err := infra.NewDBConnection(services.Database.URL, string(cfg.Environment))
	if err != nil {
		logger.Fatal("Create db connection failed", "err", err)
	}

	// start kvstore
	kvstore, err := kvstore.NewFromConfig(services.KVS)
	if err != nil {
		logger.Fatal("Create kvstore failed", "err", err)
	}
	defer kvstore.Close()

	natsConn, err := infra.GetNATSConnection(services.Nats, string(cfg.Environment))
	if err != nil {
		logger.Fatal("Create NATS connection failed", "err", err)
	}
	defer natsConn.Close()

	transferEventQueueManager := infra.NewNATsMessageQueueManager("transfer", []string{
		"transfer.event.*",
	}, natsConn)

	transferQueue := transferEventQueueManager.NewMessageQueue("dispatch")

	emitter := events.NewEmitter(transferQueue, services.Nats.SubjectPrefix)
	defer emitter.Close()

	// start address bloom filter
	addressBF := addressbloomfilter.NewBloomFilter(services.Bloomfilter, db, redisClient)
	if err := addressBF.Initialize(ctx); err != nil {
		logger.Fatal("Address bloom filter init failed", "err", err)
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

	// Start all workers
	logger.Info("Starting all workers")
	manager.Start()

	logger.Info("🚀 Transaction indexer is running... Press Ctrl+C to stop")
	waitForShutdown()

	logger.Info("Shutting down indexer...")
	manager.Stop()
	logger.Info("✅ Indexer stopped gracefully")
}

func waitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
