package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fystack/indexer/internal/config"
	"github.com/fystack/indexer/internal/indexer"
	"github.com/fystack/indexer/internal/logger"
)

func main() {
	var chainName string
	var configPath string

	// CLI flags
	flag.StringVar(&chainName, "chain", "", "Chain to index (e.g. tron, evm)")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to configuration file")
	flag.Parse()

	if chainName == "" {
		slog.Error("Missing required -chain flag (e.g. -chain tron)")
		os.Exit(1)
	}

	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("Failed to load config", "err", err)
		os.Exit(1)
	}

	logger.Init(&logger.Options{
		Level:      slog.LevelDebug,
		TimeFormat: time.RFC3339,
	})

	slog.Info("config loaded")

	// Create and start indexer manager
	manager, err := indexer.NewManager(cfg)
	if err != nil {
		slog.Error("Failed to create indexer manager", "error", err)
	}

	if err := manager.StartSelectedChain(chainName); err != nil {
		slog.Error("Failed to start indexer", "error", err)
	}

	// Wait for interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	slog.Info("Indexer is running. Press Ctrl+C to stop.")
	<-c

	slog.Info("Shutting down indexer...")
	manager.Stop()
	slog.Info("Indexer stopped.")
}
