package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/nats-io/nats.go"

	"github.com/fystack/transaction-indexer/internal/worker"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/fystack/transaction-indexer/pkg/infra"
)

type CLI struct {
	Index       IndexCmd       `cmd:"" help:"Run the indexer (regular + optional catchup)."`
	NATSPrinter NATSPrinterCmd `cmd:"" name:"nats-printer" help:"Print NATS messages."`
}

type IndexCmd struct {
	Chains     []string `help:"Chains to index (if empty, all configured chains will be used)" sep:"," name:"chain"`
	ConfigPath string   `help:"Path to config file." default:"configs/config.yaml" name:"config"`
	Debug      bool     `help:"Enable debug logs." name:"debug"`
	Catchup    bool     `help:"Run catchup alongside regular indexer." name:"catchup"`
	Latest     bool     `help:"Start from latest block (overrides config)." name:"latest"`
}

type NATSPrinterCmd struct {
	NATSURL string `help:"NATS server URL." default:"nats://127.0.0.1:4222" name:"nats-url"`
	Subject string `help:"NATS subject to subscribe to." default:"indexer.transaction" name:"subject"`
}

func (c *IndexCmd) Run() error {
	runIndexer(c.Chains, c.ConfigPath, c.Debug, c.Catchup, c.Latest)
	return nil
}

func (c *NATSPrinterCmd) Run() error {
	runNatsPrinter(c.NATSURL, c.Subject)
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

func runIndexer(chains []string, configPath string, debug, catchup, latest bool) {
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

	// If no chains specified, use all configured chains
	if len(chains) == 0 {
		chains = getAllChainNames(&cfg)
		logger.Info("No chains specified, using all configured chains", "chains", chains)
	}

	// Validate chains
	if err := cfg.ValidateChains(chains); err != nil {
		logger.Fatal("Validate chains failed", "err", err)
	}

	// Override from_latest from CLI if requested
	if latest {
		for _, name := range chains {
			if cc, err := cfg.GetChain(name); err == nil {
				cc.FromLatest = true
				cfg.Chains.Items[name] = cc
			}
		}
		logger.Info("Overriding start to latest for chains", "chains", chains)
	}

	// start redis
	redisClient, err := infra.NewRedisClient(cfg.Redis.URL, cfg.Redis.Password, cfg.Environment.String())
	if err != nil {
		logger.Fatal("Create redis client failed", "err", err)
	}
	infra.InitGlobalRedisClient(redisClient)

	// start db
	db, err := infra.NewDBConnection(cfg.DB.URL, cfg.Environment.String())
	if err != nil {
		logger.Fatal("Create db connection failed", "err", err)
	}
	infra.InitGlobalDB(db)

	manager, err := worker.NewManager(ctx, &cfg)
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
	if catchup {
		if err := manager.Start(chains, worker.ModeCatchup); err != nil {
			logger.Fatal("Start catchup failed", "err", err)
		}
	}

	logger.Info("Indexer is running... Press Ctrl+C to stop")
	waitForShutdown()
	manager.Stop()
	logger.Info("Indexer stopped")
}

func runNatsPrinter(natsURL, subject string) {
	level := slog.LevelInfo
	logger.Init(&logger.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})

	nc, err := nats.Connect(natsURL)
	if err != nil {
		logger.Fatal("NATS connect failed", "err", err)
	}
	defer nc.Close()

	logger.Info("Subscribed to", "subject", subject)

	_, err = nc.Subscribe(subject, func(msg *nats.Msg) {
		var txn types.Transaction
		if err := txn.UnmarshalBinary(msg.Data); err != nil {
			logger.Fatal("Unmarshal error", "err", err)
		}
		logger.Info("Received transaction", "txn", txn.String())
	})
	if err != nil {
		logger.Fatal("NATS subscribe failed", "err", err)
	}

	select {} // Block forever
}

func waitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}

// getAllChainNames returns all configured chain names from the config
func getAllChainNames(cfg *config.Config) []string {
	var chainNames []string
	for chainName := range cfg.Chains.Items {
		chainNames = append(chainNames, chainName)
	}
	return chainNames
}
