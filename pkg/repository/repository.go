package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	// DBX: Database Error
	ErrGeneric error = errors.New("DBX: Internal server error")

	// DBXO: Bad operation
	// DBXQ: Bad query
	ErrDuplicate        error = errors.New("DBXO: Duplicate")
	ErrNotFound         error = errors.New("DBXQ: Not found")
	ErrRelationNotExist error = errors.New("DBXO: Relation not exists")
)

var (
	// Class 23 — Integrity Constraint Violation
	// https://github.com/jackc/pgerrcode/blob/master/errcode.go
	UniqueViolation     = "23505"
	ForeignKeyViolation = "23503"
)

type Repository[T any] interface {
	Find(ctx context.Context, options FindOptions) ([]*T, error)
}

// gorm generic repository
type repository[T any] struct {
	db *gorm.DB
}

func NewRepository[T any](db *gorm.DB) Repository[T] {
	return &repository[T]{
		db: db,
	}
}

func (r *repository[T]) handleDBError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// skip if is error record not found
		// let the caller handle it
		return nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case UniqueViolation:
			return ErrDuplicate
		case ForeignKeyViolation:
			return ErrRelationNotExist
		default:
			return nil
		}

	}
	return nil
}

func (r *repository[T]) WrapError(ctx context.Context, err error) error {
	// If error is wrapped, return wrapped error
	handledErr := r.handleDBError(err)
	if handledErr != nil {
		return handledErr
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// skip if is error record not found
		return nil
	}

	// otherwise, return original error
	// this is usually an unidentified internal error
	if err != nil {
		return ErrGeneric
	}

	return nil
}

func (r *repository[T]) HealthCheck() error {
	if err := r.db.Exec("SELECT 1").Error; err != nil {
		return errors.New("DB is not healthy")
	}

	return nil
}

func (r *repository[T]) applyFindOptionsToDB(db *gorm.DB, options FindOptions) *gorm.DB {
	isSelectAll := len(options.Select) == 1 && options.Select[0] == "*"
	if options.Select != nil && !isSelectAll {
		db = db.Select(strings.Join(options.Select, ","))
	}

	if options.Where != nil {
		db = db.Where(map[string]any(options.Where))
	}

	if options.Order != nil {
		var orders string
		for field, order := range options.Order {
			orders += fmt.Sprintf("%s %s,", field, order)
		}
		orders = strings.TrimSuffix(orders, ",")
		db = db.Order(orders)
	}

	if options.Limit != 0 {
		db = db.Limit(int(options.Limit))
	}

	if options.Offset != 0 {
		db = db.Offset(int(options.Offset))
	}

	if options.Relations != nil {
		for relation, fields := range options.Relations {
			cFields := fields // copy fields to avoid closure
			db = db.Preload(relation, func(tx *gorm.DB) *gorm.DB {
				hasID := false
				for _, field := range cFields {
					parts := strings.Split(field, ",") // some fields are declared as "field,field2"
					if slices.Contains(parts, "id") || field == "*" {
						hasID = true
						break
					}
				}
				if !hasID {
					cFields = append(cFields, "id")
				}
				if options.RelationFilters != nil {
					if filter, ok := options.RelationFilters[relation]; ok && filter != nil {
						tx = tx.Where(map[string]any(filter))
					}
				}
				return tx.Select([]string(cFields))
			})
		}
	}

	return db
}

func (r *repository[T]) Find(ctx context.Context, options FindOptions) ([]*T, error) {
	var results []*T
	db := r.db.WithContext(ctx).Model(results)
	r.applyFindOptionsToDB(db, options)

	if err := db.Find(&results).Error; err != nil {
		err = r.WrapError(ctx, err)
		return results, err
	}

	return results, nil
}

func (r *repository[T]) Count(ctx context.Context, options FindOptions) (int64, error) {
	var count int64
	var entity T
	db := r.db.WithContext(ctx).Model(&entity)
	if options.Where != nil {
		db = db.Where(map[string]any(options.Where))
	}

	err := db.Count(&count).Error
	if err != nil {
		return 0, r.WrapError(ctx, err)
	}

	return count, nil
}

func (r *repository[T]) Transaction(
	ctx context.Context,
	fn func(txRepo Repository[T]) error,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := NewRepository[T](tx)
		return fn(txRepo)
	})
}

func (r *repository[T]) GetDB() *gorm.DB {
	return r.db
}
