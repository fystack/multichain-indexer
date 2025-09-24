package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/alecthomas/kong"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/kvstore"
	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/fystack/multichain-indexer/pkg/repository"
	"gorm.io/gorm"
)

type CLI struct {
	Run RunCmd `cmd:"" help:"Load wallet addresses from DB into Consul KV."`
}

type RunCmd struct {
	ConfigPath string `help:"Path to config file."    default:"configs/config.yaml" name:"config"`
	BatchSize  int    `help:"DB batch size per page." default:"1000"                name:"batch"`
	Debug      bool   `help:"Enable debug logs."                                    name:"debug"`
}

func (c *RunCmd) Run() error {
	ctx := context.Background()

	level := slog.LevelInfo
	if c.Debug {
		level = slog.LevelDebug
	}
	logger.Init(&logger.Options{Level: level, TimeFormat: time.RFC3339})

	cfg, err := config.Load(c.ConfigPath)
	if err != nil {
		logger.Fatal("Load config failed", "err", err)
	}

	// Init DB (required for repository)
	db, err := infra.NewDBConnection(cfg.Services.Database.URL, string(cfg.Environment))
	if err != nil {
		logger.Fatal("Create db connection failed", "err", err)
	}

	// Build KV store from config; must be consul
	if cfg.Services.KVS.Type != enum.KVStoreTypeConsul {
		logger.Fatal("KVStore type must be consul for this command", "type", cfg.Services.KVS.Type)
	}
	store, err := kvstore.NewFromConfig(cfg.Services.KVS)
	if err != nil {
		logger.Fatal("Create KV store failed", "err", err)
	}
	defer store.Close()

	// Discover supported address_type enum labels from DB to avoid SQLSTATE 22P02
	supportedTypes, err := fetchSupportedAddressTypes(db)
	if err != nil {
		logger.Fatal("Fetch supported address types failed", "err", err)
	}

	repo := repository.NewRepository[model.WalletAddress](db)
	batch := c.BatchSize
	if batch <= 0 {
		batch = 1000
	}

	candidateTypes := []enum.NetworkType{
		enum.NetworkTypeTron,
		enum.NetworkTypeEVM,
	}

	for _, t := range candidateTypes {
		if _, ok := supportedTypes[string(t)]; !ok {
			logger.Warn("Skipping unsupported address_type in DB enum", "type", t)
			continue
		}

		var offset int
		totalWritten := 0
		for {
			rows, err := repo.Find(ctx, repository.FindOptions{
				Where:  repository.WhereType{"type": t},
				Select: []string{"address"},
				Limit:  uint(batch),
				Offset: uint(offset),
			})
			if err != nil {
				logger.Fatal("DB query failed", "err", err)
			}
			if len(rows) == 0 {
				break
			}

			for _, w := range rows {
				key := string(t) + "/" + w.Address
				if err := store.Set(key, "ok"); err != nil {
					logger.Fatal("KV set failed", "key", key, "err", err)
				}
			}
			offset += len(rows)
			totalWritten += len(rows)
			logger.Info("Backfilled batch", "type", t, "written", len(rows), "offset", offset)
		}
		logger.Info("Backfill complete for type", "type", t, "total_written", totalWritten)
	}

	logger.Info("Done")
	return nil
}

// fetchSupportedAddressTypes queries PostgreSQL for enum labels of address_type
func fetchSupportedAddressTypes(db *gorm.DB) (map[string]struct{}, error) {
	// Use the underlying *sql.DB for raw query
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	rows, err := sqlDB.Query(`
		SELECT e.enumlabel
		FROM pg_type t
		JOIN pg_enum e ON t.oid = e.enumtypid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
		WHERE t.typname = 'address_type'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	labels := map[string]struct{}{}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels[label] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return labels, nil
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("wallet-kv-load"),
		kong.Description("Load wallet addresses from DB to Consul KV."),
		kong.UsageOnError(),
	)
	if err := ctx.Run(); err != nil {
		os.Exit(1)
	}
}
