package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
	"github.com/goccy/go-yaml"
	"github.com/hashicorp/consul/api"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// Emojis for different states
const (
	emojiRocket   = "🚀"
	emojiCheck    = "✅"
	emojiWarning  = "⚠️"
	emojiError    = "❌"
	emojiDatabase = "🗄️"
	emojiMigrate  = "🔄"
	emojiComplete = "🎉"
	emojiSearch   = "🔍"
	emojiProgress = "📊"
)

type EndpointType string

const (
	EndpointTypeBadger EndpointType = "badger"
	EndpointTypeConsul EndpointType = "consul"
)

type CLI struct {
	Config string `help:"YAML config file for migration"               required:"true"`
	DryRun bool   `help:"dry run mode (print actions without writing)"`
}

type MigrationConfig struct {
	Source      EndpointConfig `yaml:"source"`
	Destination EndpointConfig `yaml:"destination"`
	Prefixes    []string       `yaml:"prefixes"`
	Verify      bool           `yaml:"verify"`
}

type EndpointConfig struct {
	Type   enum.KVStoreType    `yaml:"type"`
	Badger config.BadgerConfig `yaml:"badger,omitempty"`
	Consul config.ConsulConfig `yaml:"consul,omitempty"`
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("kv-migrate"),
		kong.Description("Migrate keys between Badger and Consul KV stores"))

	printBanner()

	cfg, err := loadConfig(cli.Config)
	ctx.FatalIfErrorf(err)

	if len(cfg.Prefixes) == 0 {
		ctx.Fatalf("at least one prefix is required in config")
	}

	printConfig(cfg, cli.DryRun)

	src, err := buildStore(cfg.Source)
	ctx.FatalIfErrorf(err)
	defer src.Close()

	dst, err := buildStore(cfg.Destination)
	ctx.FatalIfErrorf(err)
	defer dst.Close()

	startTime := time.Now()
	total, copied, err := migrate(src, dst, cfg.Prefixes, cfg.Verify, cli.DryRun)
	ctx.FatalIfErrorf(err)
	duration := time.Since(startTime)

	printSummary(total, copied, duration, cli.DryRun)
}

func printBanner() {
	fmt.Printf("%s%s%s %s KV Store Migration Tool %s%s%s\n",
		colorBold, colorCyan, emojiRocket, emojiDatabase, emojiMigrate, colorReset, "\n")
}

func printConfig(cfg *MigrationConfig, dryRun bool) {
	fmt.Printf("%s%s Configuration:%s\n", colorBold, colorBlue, colorReset)
	fmt.Printf("  %s Source: %s%s%s\n", emojiSearch, colorYellow, cfg.Source.Type, colorReset)
	fmt.Printf(
		"  %s Destination: %s%s%s\n",
		emojiSearch,
		colorYellow,
		cfg.Destination.Type,
		colorReset,
	)
	fmt.Printf(
		"  %s Prefixes: %s%s%s\n",
		emojiSearch,
		colorYellow,
		strings.Join(cfg.Prefixes, ", "),
		colorReset,
	)
	fmt.Printf("  %s Verify: %s%v%s\n", emojiSearch, colorYellow, cfg.Verify, colorReset)
	if dryRun {
		fmt.Printf("  %s Mode: %sDRY RUN%s\n", emojiWarning, colorRed, colorReset)
	}
	fmt.Println()
}

func printSummary(total, copied int, duration time.Duration, dryRun bool) {
	fmt.Println()
	fmt.Printf("%s%s Migration Summary:%s\n", colorBold, colorGreen, colorReset)

	if dryRun {
		fmt.Printf(
			"  %s Would migrate: %s%d%s keys\n",
			emojiProgress,
			colorYellow,
			total,
			colorReset,
		)
	} else {
		fmt.Printf("  %s Total keys: %s%d%s\n", emojiProgress, colorYellow, total, colorReset)
		fmt.Printf("  %s Copied: %s%d%s\n", emojiCheck, colorGreen, copied, colorReset)
	}

	fmt.Printf(
		"  %s Duration: %s%s%s\n",
		emojiProgress,
		colorYellow,
		duration.Round(time.Millisecond),
		colorReset,
	)

	if !dryRun && total > 0 {
		rate := float64(copied) / duration.Seconds()
		fmt.Printf("  %s Rate: %s%.1f keys/sec%s\n", emojiProgress, colorYellow, rate, colorReset)
	}

	if dryRun {
		fmt.Printf(
			"\n%s%s Dry run completed - no data was modified%s\n",
			colorBold,
			colorYellow,
			colorReset,
		)
	} else {
		fmt.Printf("\n%s%s Migration completed successfully! %s%s\n", colorBold, colorGreen, emojiComplete, colorReset)
	}
}

func loadConfig(path string) (*MigrationConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg MigrationConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}

	return &cfg, nil
}

func buildStore(config EndpointConfig) (infra.KVStore, error) {
	switch config.Type {
	case enum.KVStoreTypeBadger:
		if config.Badger.Directory == "" {
			return nil, fmt.Errorf("badger directory is required")
		}
		return kvstore.NewBadgerStore(
			config.Badger.Directory,
			config.Badger.Prefix,
			infra.JSON,
		)
	case enum.KVStoreTypeConsul:
		if config.Consul.Address == "" {
			return nil, fmt.Errorf("consul address is required")
		}
		var httpAuth *api.HttpBasicAuth
		if config.Consul.HttpAuth.Username != "" || config.Consul.HttpAuth.Password != "" {
			httpAuth = &api.HttpBasicAuth{
				Username: config.Consul.HttpAuth.Username,
				Password: config.Consul.HttpAuth.Password,
			}
		}
		return kvstore.NewConsulClient(kvstore.Options{
			Scheme:   config.Consul.Scheme,
			Address:  config.Consul.Address,
			Folder:   config.Consul.Folder,
			Codec:    infra.JSON,
			Token:    config.Consul.Token,
			HttpAuth: httpAuth,
		})
	default:
		return nil, fmt.Errorf("unsupported store type: %s", config.Type)
	}
}

func migrate(src, dst infra.KVStore, prefixes []string, verify, dryRun bool) (int, int, error) {
	var allPairs []*infra.KVPair

	fmt.Printf("%s%s Scanning source store...%s\n", colorBold, colorBlue, colorReset)

	// Collect all key-value pairs from source
	for _, prefix := range prefixes {
		fmt.Printf("  %s Scanning prefix: %s%s%s\n", emojiSearch, colorCyan, prefix, colorReset)
		pairs, err := src.List(prefix)
		if err != nil {
			return 0, 0, fmt.Errorf("listing keys with prefix %q: %w", prefix, err)
		}
		fmt.Printf("    %s Found %s%d%s keys\n", emojiCheck, colorGreen, len(pairs), colorReset)
		allPairs = append(allPairs, pairs...)
	}

	total := len(allPairs)
	fmt.Printf(
		"\n%s%s Total keys to migrate: %s%d%s\n",
		emojiProgress,
		colorBold,
		colorYellow,
		total,
		colorReset,
	)

	if total == 0 {
		fmt.Printf("%s%s No keys found to migrate%s\n", emojiWarning, colorYellow, colorReset)
		return 0, 0, nil
	}

	if dryRun {
		fmt.Printf(
			"\n%s%s DRY RUN - Keys that would be migrated:%s\n",
			emojiWarning,
			colorRed,
			colorReset,
		)
		for i, kv := range allPairs {
			fmt.Printf("  %s %s\n", emojiMigrate, kv.Key)
			if i >= 9 && len(allPairs) > 10 {
				fmt.Printf("  %s ... and %d more keys\n", emojiProgress, len(allPairs)-10)
				break
			}
		}
		return total, 0, nil
	}

	fmt.Printf("\n%s%s Starting migration...%s\n", emojiRocket, colorBold, colorReset)

	// Migrate each key-value pair
	copied := 0
	for i, kv := range allPairs {
		if err := dst.Set(kv.Key, string(kv.Value)); err != nil {
			return total, copied, fmt.Errorf("setting key %q: %w", kv.Key, err)
		}
		copied++

		// Verify if requested
		if verify {
			got, err := dst.Get(kv.Key)
			if err != nil {
				return total, copied, fmt.Errorf("verifying key %q: %w", kv.Key, err)
			}
			if got != string(kv.Value) {
				return total, copied, fmt.Errorf(
					"verification failed for key %q: expected %q, got %q",
					kv.Key,
					string(kv.Value),
					got,
				)
			}
		}

		// Show progress with cool formatting
		if (i+1)%100 == 0 || i == total-1 {
			progress := float64(i+1) / float64(total) * 100
			bar := createProgressBar(progress, 20)
			fmt.Printf("\r%s%s Progress: %s %s%.1f%%%s (%d/%d)",
				emojiProgress, colorBold, bar, colorGreen, progress, colorReset, i+1, total)
		}
	}
	fmt.Println() // New line after progress bar

	return total, copied, nil
}

func createProgressBar(progress float64, width int) string {
	filled := int(progress / 100 * float64(width))
	bar := strings.Repeat("█", filled)
	empty := strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s%s]", bar, empty)
}
