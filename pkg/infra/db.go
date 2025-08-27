package infra

import (
	"sync"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"github.com/fystack/transaction-indexer/pkg/common/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	globalDB *gorm.DB
	dbOnce   sync.Once
)

// InitGlobalDB sets the DB client only once.
func InitGlobalDB(c *gorm.DB) {
	dbOnce.Do(func() {
		globalDB = c
	})
}

// GlobalDB returns the global DB client (may be nil).
func GlobalDB() *gorm.DB {
	return globalDB
}

// MustGlobalDB returns the global DB client or panics if not initialized.
func MustGlobalDB() *gorm.DB {
	if globalDB == nil {
		logger.Fatal("global DB not initialized")
	}
	return globalDB
}

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
