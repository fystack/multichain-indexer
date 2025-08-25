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

	"github.com/fystack/transaction-indexer/internal/indexer"
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
	Chain      string `help:"Chain to index." required:"" name:"chain"`
	ConfigPath string `help:"Path to config file." default:"configs/config.yaml" name:"config"`
	Debug      bool   `help:"Enable debug logs." name:"debug"`
	Catchup    bool   `help:"Run catchup alongside regular indexer." name:"catchup"`
}

type NATSPrinterCmd struct {
	NATSURL string `help:"NATS server URL." default:"nats://127.0.0.1:4222" name:"nats-url"`
	Subject string `help:"NATS subject to subscribe to." default:"indexer.transaction" name:"subject"`
}

func (c *IndexCmd) Run() error {
	runIndexer(c.Chain, c.ConfigPath, c.Debug, c.Catchup)
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

func runIndexer(chain, configPath string, debug, catchup bool) {
	ctx := context.Background()
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("Load config failed", "err", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logger.Init(&logger.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})
	slog.Info("Config loaded")

	// start redis
	redisClient := infra.NewRedisClient(cfg.Redis.URL, cfg.Redis.Password, cfg.Redis.Environment)
	infra.SetGlobalRedisClient(redisClient)

	// start db
	db, err := infra.NewDBConnection(cfg.DB.URL, cfg.DB.Environment)
	if err != nil {
		slog.Error("Create db connection failed", "err", err)
		os.Exit(1)
	}
	infra.SetGlobalDB(db)

	manager, err := indexer.NewManager(ctx, &cfg)
	if err != nil {
		slog.Error("Create indexer manager failed", "err", err)
		os.Exit(1)
	}

	// start regular worker
	if err := manager.Start(chain); err != nil {
		slog.Error("Start regular worker failed", "err", err)
		os.Exit(1)
	}

	// optionally run catchup
	if catchup {
		if err := manager.StartCatchupAuto(chain); err != nil {
			slog.Error("Start catchup failed", "err", err)
			os.Exit(1)
		}
	}

	slog.Info("Indexer is running... Press Ctrl+C to stop")
	waitForShutdown()
	manager.Stop()
	slog.Info("Indexer stopped")
}

func runNatsPrinter(natsURL, subject string) {
	level := slog.LevelInfo
	logger.Init(&logger.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})

	nc, err := nats.Connect(natsURL)
	if err != nil {
		slog.Error("NATS connect failed", "err", err)
		os.Exit(1)
	}
	defer nc.Close()

	slog.Info("Subscribed to", "subject", subject)

	_, err = nc.Subscribe(subject, func(msg *nats.Msg) {
		var txn types.Transaction
		if err := txn.UnmarshalBinary(msg.Data); err != nil {
			slog.Error("Unmarshal error", "err", err)
			return
		}
		slog.Info("Received transaction", "txn", txn.String())
	})
	if err != nil {
		slog.Error("NATS subscribe failed", "err", err)
		os.Exit(1)
	}

	select {} // Block forever
}

func waitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
