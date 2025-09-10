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
	Index IndexCmd `cmd:"" help:"Run the indexer (regular + optional catchup)."`
}

type IndexCmd struct {
	Chains     []string `help:"Chains to index (if empty, all configured chains will be used)" sep:"," name:"chain"`
	ConfigPath string   `help:"Path to config file." default:"configs/config.yaml" name:"config"`
	Debug      bool     `help:"Enable debug logs." name:"debug"`
	Manual     bool     `help:"Run manual indexer." name:"manual"`
	Catchup    bool     `help:"Run catchup alongside regular indexer." name:"catchup"`
	Latest     bool     `help:"Start from latest block (overrides config)." name:"latest"`
}

func (c *IndexCmd) Run() error {
	runIndexer(c.Chains, c.ConfigPath, c.Debug, c.Manual, c.Catchup, c.Latest)
	return nil
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("indexer"),
		kong.Description("Multi-chain transaction indexer & NATS log printer."),
		kong.UsageOnError(),
	)
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}

func runIndexer(chains []string, configPath string, debug, manual, catchup, latest bool) {
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
		logger.Fatal("Load config failed", "err", err)
	}
	logger.Info("Config loaded", "environment", cfg.Environment)

	// start redis
	redisClient, err := infra.NewRedisClient(cfg.Redis.URL, cfg.Redis.Password, cfg.Environment)
	if err != nil {
		logger.Fatal("Create redis client failed", "err", err)
	}

	// start db
	db, err := infra.NewDBConnection(cfg.DB.URL, cfg.Environment)
	if err != nil {
		logger.Fatal("Create db connection failed", "err", err)
	}

	// start kvstore
	kvstore, err := kvstore.NewFromConfig(cfg.KVStore)
	if err != nil {
		logger.Fatal("Create kvstore failed", "err", err)
	}
	defer kvstore.Close()

	// start emitter
	emitter, err := events.NewEmitter(cfg.NATS.URL, cfg.NATS.SubjectPrefix)
	if err != nil {
		logger.Fatal("Create emitter failed", "err", err)
	}
	defer emitter.Close()

	// start address bloom filter
	addressBF := addressbloomfilter.NewBloomFilter(cfg.BloomFilter, db, redisClient)
	if err := addressBF.Initialize(ctx); err != nil {
		logger.Fatal("Address bloom filter init failed", "err", err)
	}

	// If no chains specified, use all configured chains
	if len(chains) == 0 {
		chains = cfg.Chains.GetAllChainNames()
		logger.Info("No chains specified, using all configured chains", "chains", chains)
	}

	// Validate chains
	if err := cfg.Chains.ValidateChains(chains); err != nil {
		logger.Fatal("Validate chains failed", "err", err)
	}

	// Override from_latest from CLI if requested
	if latest {
		cfg.Chains.OverrideFromLatest(chains)
		logger.Info("Overriding start to latest for chains", "chains", chains)
	}

	manager, err := worker.NewManager(ctx, &cfg, db, kvstore, addressBF, emitter, redisClient)
	if err != nil {
		logger.Fatal("Create indexer manager failed", "err", err)
	}

	// start regular worker
	if err := manager.Start(chains, worker.ModeRegular); err != nil {
		logger.Fatal("Start regular worker failed", "err", err)
	}

	// start rescanner worker
	if err := manager.Start(chains, worker.ModeRescanner); err != nil {
		logger.Fatal("Start rescanner worker failed", "err", err)
	}

	// optionally run catchup
	if catchup || cfg.Worker.Catchup.Enabled {
		if err := manager.Start(chains, worker.ModeCatchup); err != nil {
			logger.Fatal("Start catchup failed", "err", err)
		}
	}

	// optionally run manual
	if manual || cfg.Worker.Manual.Enabled {
		if err := manager.Start(chains, worker.ModeManual); err != nil {
			logger.Fatal("Start manual failed", "err", err)
		}
	}

	logger.Info("Indexer is running... Press Ctrl+C to stop")
	waitForShutdown()
	manager.Stop()
	logger.Info("Indexer stopped")
}

func waitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
