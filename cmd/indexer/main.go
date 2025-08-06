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
	rootCmd := setupRootCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func setupRootCmd() *cobra.Command {
	var (
		chainName  string
		configPath string
		natsURL    string
		subject    string
		isLatest   bool
		debug      bool
	)

	rootCmd := &cobra.Command{
		Use:   "indexer",
		Short: "Unified blockchain indexer CLI",
	}

	indexCmd := &cobra.Command{
		Use:   "index",
		Short: "Run the indexer",
		Run: func(cmd *cobra.Command, args []string) {
			runIndexer(chainName, configPath, isLatest, debug)
		},
	}
	indexCmd.Flags().StringVar(&chainName, "chain", "", "Chain to index (e.g. tron, evm, or empty for all)")
	indexCmd.Flags().BoolVar(&isLatest, "latest", false, "Index from the latest block")
	indexCmd.Flags().StringVar(&configPath, "config", "configs/config.yaml", "Path to configuration file")
	indexCmd.Flags().BoolVar(&debug, "d", false, "Enable debug mode")

	natsPrinterCmd := &cobra.Command{
		Use:   "nats-printer",
		Short: "Run the NATS event printer",
		Run: func(cmd *cobra.Command, args []string) {
			runNatsPrinter(natsURL, subject)
		},
	}
	natsPrinterCmd.Flags().StringVar(&natsURL, "nats-url", nats.DefaultURL, "NATS server URL")
	natsPrinterCmd.Flags().StringVar(&subject, "subject", "indexer.transaction", "NATS subject to subscribe to")

	rootCmd.AddCommand(indexCmd, natsPrinterCmd)
	return rootCmd
}

func runIndexer(chainName, configPath string, isLatest, debug bool) {
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("Failed to load config", "err", err)
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
	if chainName != "" {
		chainConfig := cfg.Indexer.Chains[chainName]
		chainConfig.IsLatest = isLatest
		cfg.Indexer.Chains[chainName] = chainConfig
	}
	manager, err := indexer.NewManager(cfg)
	if err != nil {
		slog.Error("Failed to create indexer manager", "error", err)
		os.Exit(1)
	}

	if err := manager.Start(chainName); err != nil {
		slog.Error("Failed to start indexer", "error", err)
		os.Exit(1)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	slog.Info("Indexer is running. Press Ctrl+C to stop.")
	<-c

	slog.Info("Shutting down indexer...")
	manager.Stop()
	slog.Info("Indexer stopped.")
}

func runNatsPrinter(natsURL, subject string) {
	nc, err := nats.Connect(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS: %v\n", err)
		os.Exit(1)
	}
	defer nc.Close()

	fmt.Printf("Subscribed to subject: %s\n", subject)

	_, err = nc.Subscribe(subject, func(msg *nats.Msg) {
		txn := types.Transaction{}
		err := txn.UnmarshalBinary(msg.Data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to unmarshal transaction: %v\n", err)
			return
		}
		fmt.Printf("[%s] %s\n", msg.Subject, txn.String())
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to subscribe: %v\n", err)
		os.Exit(1)
	}

	select {} // Block forever
}
