package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fystack/multichain-indexer/internal/worker"
	"github.com/fystack/multichain-indexer/pkg/addressbloomfilter"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/kvstore"
)

func main() {
	var (
		configPath string
		chainsList string
		debug      bool
		catchup    bool
	)

	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to configuration file")
	flag.StringVar(&chainsList, "chains", "", "Comma-separated list of chains to test (required)")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.BoolVar(&catchup, "catchup", false, "Enable catchup worker alongside regular worker")
	flag.Parse()

	if chainsList == "" {
		fmt.Fprintln(os.Stderr, "missing required --chains flag (e.g. --chains=ethereum_mainnet)")
		os.Exit(1)
	}
	chains := strings.Split(chainsList, ",")
	for i := range chains {
		chains[i] = strings.TrimSpace(chains[i])
	}

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
		logger.Fatal("Failed to load config", "err", err)
	}

	if err := cfg.Chains.Validate(chains); err != nil {
		logger.Fatal("Invalid chains", "err", err)
	}

	services := cfg.Services

	// Redis
	redisClient, err := infra.NewRedisClient(
		services.Redis.URL,
		services.Redis.Password,
		string(cfg.Environment),
		services.Redis.MTLS,
	)
	if err != nil {
		logger.Fatal("Redis connection failed", "err", err)
	}

	// KVStore
	kv, err := kvstore.NewFromConfig(services.KVS)
	if err != nil {
		logger.Fatal("KVStore connection failed", "err", err)
	}
	defer kv.Close()

	// NATS
	natsConn, err := infra.GetNATSConnection(services.Nats, string(cfg.Environment))
	if err != nil {
		logger.Fatal("NATS connection failed", "err", err)
	}
	defer natsConn.Close()

	eventQueueManager := infra.NewNATsMessageQueueManager("transfer", []string{
		"transfer.event.*",
	}, natsConn)
	eventQueue := eventQueueManager.NewMessageQueue("dispatch")

	utxoQueueManager := infra.NewNATsMessageQueueManager("utxo", []string{
		"utxo.event.*",
	}, natsConn)
	utxoQueue := utxoQueueManager.NewMessageQueue("dispatch")

	emitter := events.NewEmitter(eventQueue, utxoQueue, services.Nats.SubjectPrefix)
	defer emitter.Close()

	// Address bloom filter (optional)
	var addressBF addressbloomfilter.WalletAddressBloomFilter
	if services.Bloomfilter != nil {
		addressBF = addressbloomfilter.NewBloomFilter(*services.Bloomfilter, nil, redisClient)
		logger.Info("Address bloom filter created (no DB initialization)")
	}

	// Create tracker
	tracker := NewTracker()

	ctx, cancel := context.WithCancel(context.Background())

	managerCfg := worker.ManagerConfig{
		Chains:        chains,
		EnableRegular: true,
		EnableCatchup: catchup,
		Observer:      tracker.Observe,
	}

	manager := worker.CreateManagerWithWorkers(
		ctx,
		cfg,
		kv,
		nil, // no DB needed for devtool
		addressBF,
		emitter,
		redisClient,
		managerCfg,
	)

	logger.Info("Starting workers for block reliability test", "chains", chains)
	manager.Start()

	fmt.Println("\nPress Ctrl+C or Enter to stop and generate report...")

	// Wait for signal or Enter
	done := make(chan struct{}, 1)
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		select {
		case done <- struct{}{}:
		default:
		}
	}()
	go func() {
		reader := bufio.NewReader(os.Stdin)
		_, _ = reader.ReadString('\n')
		select {
		case done <- struct{}{}:
		default:
		}
	}()
	<-done

	fmt.Println("\nStopping workers...")
	cancel()
	manager.Stop()

	// Generate reports
	fmt.Println("\n========== BLOCK RELIABILITY REPORT ==========")

	for _, chainName := range chains {
		// Indexers uppercase chain names via GetName()
		upperName := strings.ToUpper(chainName)

		tracker.mu.RLock()
		ct, ok := tracker.chains[upperName]
		tracker.mu.RUnlock()

		if !ok {
			fmt.Printf("\n=== %s ===\n", upperName)
			fmt.Println("  No blocks observed")
			continue
		}

		ct.mu.Lock()
		var minBlock, maxBlock uint64
		first := true
		for b := range ct.blocks {
			if first {
				minBlock = b
				maxBlock = b
				first = false
				continue
			}
			if b < minBlock {
				minBlock = b
			}
			if b > maxBlock {
				maxBlock = b
			}
		}
		ct.mu.Unlock()

		if first {
			fmt.Printf("\n=== %s ===\n", upperName)
			fmt.Println("  No blocks observed")
			continue
		}

		report := tracker.Report(upperName, minBlock, maxBlock)
		PrintReport(report)
	}
}
