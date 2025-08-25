package infra

import (
	"sync"
	"time"

	logger "log/slog"

	"github.com/fystack/transaction-indexer/pkg/common/constant"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	globalDB   *gorm.DB
	globalDBMu sync.RWMutex
)

// SetGlobalRedisClient sets the process-wide Redis client.
func SetGlobalDB(c *gorm.DB) {
	globalDBMu.Lock()
	defer globalDBMu.Unlock()
	globalDB = c
}

// GetGlobalDB returns the process-wide DB client (may be nil).
func GetGlobalDB() *gorm.DB {
	globalDBMu.RLock()
	defer globalDBMu.RUnlock()
	return globalDB
}

// MustGlobalDB returns the global DB client or panics if not initialized.
func MustGlobalDB() *gorm.DB {
	c := GetGlobalDB()
	if c == nil {
		panic("global DB not initialized")
	}
	return c
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
