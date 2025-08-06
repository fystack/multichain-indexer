package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fystack/transaction-indexer/internal/config"
	"github.com/fystack/transaction-indexer/internal/indexer"
	"github.com/fystack/transaction-indexer/internal/logger"
	"github.com/fystack/transaction-indexer/internal/types"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

func main() {
	var (
		chainName  string
		configPath string
		natsURL    string
		subject    string
		isLatest   bool
		debug      bool
	)

	rootCmd := &cobra.Command{Use: "indexer"}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "index",
		Short: "Run the indexer",
		Run: func(cmd *cobra.Command, args []string) {
			runIndexer(chainName, configPath, isLatest, debug)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "nats-printer",
		Short: "Print NATS messages",
		Run: func(cmd *cobra.Command, args []string) {
			runNatsPrinter(natsURL, subject)
		},
	})

	rootCmd.PersistentFlags().StringVar(&chainName, "chain", "", "Chain to index")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "configs/config.yaml", "Path to config file")
	rootCmd.PersistentFlags().BoolVar(&isLatest, "latest", false, "Start from latest block")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable debug logs")

	rootCmd.PersistentFlags().StringVar(&natsURL, "nats-url", nats.DefaultURL, "NATS server URL")
	rootCmd.PersistentFlags().StringVar(&subject, "subject", "indexer.transaction", "NATS subject to subscribe to")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runIndexer(chain, configPath string, isLatest, debug bool) {
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

	manager, err := indexer.NewManager(cfg)
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
		var txn types.Transaction
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
