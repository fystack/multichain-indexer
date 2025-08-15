package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fystack/transaction-indexer/internal/core"
	"github.com/fystack/transaction-indexer/internal/indexer"

	"github.com/alecthomas/kong"
	"github.com/nats-io/nats.go"
)

type CLI struct {
	Index       IndexCmd       `cmd:"" help:"Run the indexer."`
	IndexFailed IndexFailedCmd `cmd:"" name:"index-failed" help:"Process failed blocks."`
	NATSPrinter NATSPrinterCmd `cmd:"" name:"nats-printer" help:"Print NATS messages."`
}

type IndexCmd struct {
	Chain      string `help:"Chain to index." required:"" name:"chain"`
	ConfigPath string `help:"Path to config file." default:"configs/config.yaml" name:"config"`
	Debug      bool   `help:"Enable debug logs." name:"debug"`
}

type IndexFailedCmd struct {
	Chain      string `help:"Chain to index." required:"" name:"chain"`
	ConfigPath string `help:"Path to config file." default:"configs/config.yaml" name:"config"`
	Debug      bool   `help:"Enable debug logs." name:"debug"`
	Continuous bool   `help:"Run continuously instead of one-shot." name:"continuous"`
}

type NATSPrinterCmd struct {
	NATSURL string `help:"NATS server URL." default:"nats://127.0.0.1:4222" name:"nats-url"`
	Subject string `help:"NATS subject to subscribe to." default:"indexer.transaction" name:"subject"`
	LogFile string `help:"Append logs to this file." default:"nats.log" name:"log"`
}

func (c *IndexCmd) Run() error {
	runIndexer(c.Chain, c.ConfigPath, c.Debug)
	return nil
}

func (c *IndexFailedCmd) Run() error {
	runIndexFailedBlocks(c.Chain, c.ConfigPath, c.Debug, c.Continuous)
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

func runIndexer(chain, configPath string, debug bool) {
	cfg, err := core.Load(configPath)
	if err != nil {
		slog.Error("Load config failed", "err", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	core.Init(&core.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})
	slog.Info("Config loaded")

	manager, err := indexer.NewManager(&cfg)
	if err != nil {
		slog.Error("Create indexer manager failed", "err", err)
		os.Exit(1)
	}

	if err := manager.Start(chain); err != nil {
		slog.Error("Start indexer failed", "err", err)
		os.Exit(1)
	}

	slog.Info("Indexer is running... Press Ctrl+C to stop")
	waitForShutdown()
	manager.Stop()
	slog.Info("Indexer stopped")
}

func runIndexFailedBlocks(chain, configPath string, debug, continuous bool) {
	cfg, err := core.Load(configPath)
	if err != nil {
		slog.Error("Load config failed", "err", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	core.Init(&core.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})
	slog.Info("Config loaded")

	manager, err := indexer.NewManager(&cfg)
	if err != nil {
		slog.Error("Create failed blocks indexer manager failed", "err", err)
		os.Exit(1)
	}

	// Show current failed blocks status before starting
	status, err := manager.GetFailedBlocksStatus()
	if err != nil {
		slog.Error("Failed to get failed blocks status", "err", err)
	} else {
		for chainName, count := range status {
			slog.Info("Failed blocks status", "chain", chainName, "count", count)
		}
	}

	if continuous {
		if err := manager.StartFailedBlocksContinuous(chain); err != nil {
			slog.Error("Start failed blocks (continuous) failed", "err", err)
			os.Exit(1)
		}
		slog.Info("Failed blocks indexer is running continuously... Press Ctrl+C to stop")
		waitForShutdown()
		manager.StopFailedBlocks()
		slog.Info("Failed blocks indexer stopped")
	} else {
		if err := manager.StartFailedBlocks(chain); err != nil {
			slog.Error("Start failed blocks (one-shot) failed", "err", err)
			os.Exit(1)
		}
		slog.Info("Failed blocks processing completed (one-shot mode)")
	}
}

func runNatsPrinter(natsURL, subject string) {
	level := slog.LevelInfo
	core.Init(&core.Options{
		Level:      level,
		TimeFormat: time.RFC3339,
	})
	slog.Info("Config loaded")
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Error("Create log directory failed", "err", err)
		os.Exit(1)
	}
	path := filepath.Join(logDir, "nats.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("Open log file failed", "err", err)
		os.Exit(1)
	}

	defer f.Close()

	logWriter := io.MultiWriter(os.Stdout, f)

	nc, err := nats.Connect(natsURL)
	if err != nil {
		slog.Error("NATS connect failed", "err", err)
		os.Exit(1)
	}
	defer nc.Close()

	slog.Info("Subscribed to", "subject", subject)

	_, err = nc.Subscribe(subject, func(msg *nats.Msg) {
		var txn core.Transaction
		if err := txn.UnmarshalBinary(msg.Data); err != nil {
			slog.Error("Unmarshal error", "err", err)
			return
		}
		slog.Info("Received transaction", "txn", txn.String())
		fmt.Fprintf(logWriter, "%s\n", txn.String())
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
