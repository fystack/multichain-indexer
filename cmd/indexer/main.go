package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"idx/internal/core"
	"idx/internal/indexer"

	"github.com/nats-io/nats.go"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: indexer [index|nats-printer] [flags]")
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "index":
		indexFlags := flag.NewFlagSet("index", flag.ExitOnError)
		chainName := indexFlags.String("chain", "", "Chain to index")
		configPath := indexFlags.String("config", "configs/config.yaml", "Path to config file")
		fromLatest := indexFlags.Bool("latest", false, "Start from latest block")
		debug := indexFlags.Bool("debug", false, "Enable debug logs")

		indexFlags.Parse(os.Args[2:])
		runIndexer(*chainName, *configPath, *fromLatest, *debug)

	case "nats-printer":
		natsFlags := flag.NewFlagSet("nats-printer", flag.ExitOnError)
		natsURL := natsFlags.String("nats-url", nats.DefaultURL, "NATS server URL")
		subject := natsFlags.String("subject", "indexer.transaction", "NATS subject to subscribe to")

		natsFlags.Parse(os.Args[2:])
		runNatsPrinter(*natsURL, *subject)

	default:
		fmt.Println("Unknown command:", cmd)
		fmt.Println("Available commands: index, nats-printer")
		os.Exit(1)
	}
}

func runIndexer(chain, configPath string, fromLatest, debug bool) {
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

func runNatsPrinter(natsURL, subject string) {
	nc, err := nats.Connect(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NATS connect failed: %v\n", err)
		os.Exit(1)
	}
	defer nc.Close()

	fmt.Printf("Subscribed to [%s]\n", subject)

	_, err = nc.Subscribe(subject, func(msg *nats.Msg) {
		var txn core.Transaction
		if err := txn.UnmarshalBinary(msg.Data); err != nil {
			fmt.Fprintf(os.Stderr, "Unmarshal error: %v\n", err)
			return
		}
		fmt.Printf("[%s] %s\n", msg.Subject, txn.String())
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NATS subscribe failed: %v\n", err)
		os.Exit(1)
	}

	select {} // Block forever
}

func waitForShutdown() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
