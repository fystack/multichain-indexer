package infra

import (
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func NewDBConnection(dsn string, environment string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	logger.Info("Database connection established!", "database", db.Name())

	if environment != constant.EnvProduction {
		// only print debug logs when not in production
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
