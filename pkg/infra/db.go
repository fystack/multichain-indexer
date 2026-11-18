package infra

import (
	"os"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func NewDBConnection(dsn string, environment string) (*gorm.DB, error) {
	// SQL is logged only when it fails or is slow (over 200ms): the bloom sync polls every second per network,
	// and logging each query used to fill the logs within minutes. DB_DEBUG=true logs every query again (never in
	// production).
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)})
	if err != nil {
		return nil, err
	}

	logger.Info("Database connection established!", "database", db.Name())

	if environment != constant.EnvProduction && os.Getenv("DB_DEBUG") == "true" {
		db = db.Debug()
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	// SetMaxIdleConns sets the maximum number of connections in the idle connection pool. sqlDB.SetMaxIdleConns(10)

	// axOpenConns sets the maximum number of open connections to the database.
	sqlDB.SetMaxOpenConns(100)

	// onnMaxLifetime sets the maximum amount of time a connection may be reused.
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}
