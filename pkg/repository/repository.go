package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// DBX: Database Error
	GenericError error = errors.New("DBX: Internal server error")

	// DBXO: Bad operation
	// DBXQ: Bad query
	DuplicateError   error = errors.New("DBXO: Duplicate")
	NotFoundError    error = errors.New("DBXQ: Not found")
	RelationNotExist error = errors.New("DBXO: Relation not exists")
)

var (
	// Class 23 — Integrity Constraint Violation
	// https://github.com/jackc/pgerrcode/blob/master/errcode.go
	IntegrityConstraintViolation = "23000"
	RestrictViolation            = "23001"
	NotNullViolation             = "23502"
	ForeignKeyViolation          = "23503"
	UniqueViolation              = "23505"
	CheckViolation               = "23514"
)

type Repository[T any] interface {
	FindByField(ctx context.Context, field string, value interface{}) (*T, error)
	Create(ctx context.Context, entity *T) (*T, error)
	BatchCreate(ctx context.Context, entities []*T, batchSize uint) error
	FindByID(ctx context.Context, ID string) (*T, error)
	FindByQuery(ctx context.Context, query string, args ...interface{}) (*T, error)
	FindAll(ctx context.Context) ([]*T, error)
	FindAllWhere(ctx context.Context, query string, args ...interface{}) ([]*T, error)
	Update(ctx context.Context, entity *T, fields T) (*T, error)
	UpdateByID(ctx context.Context, ID string, fields T) (*T, error)
	UpdateByIDWithMaps(ctx context.Context, ID string, fields map[string]interface{}) (*T, error)
	UpdateAllWhere(ctx context.Context, where map[string]interface{}, updates map[string]interface{}) (int64, error)
	// Soft deleteHard
	DeleteByID(ctx context.Context, ID string) error
	DeleteWhere(ctx context.Context, where map[string]interface{}) error
	// Hard delete
	HardDeleteByID(ctx context.Context, ID string) error
	FindOne(ctx context.Context, options FindOptions) (*T, error)
	Find(ctx context.Context, options FindOptions) ([]*T, error)
	Count(ctx context.Context, options FindOptions) (int64, error)
	Transaction(ctx context.Context, fn func(txRepo Repository[T]) error) error
	// Wrap raw error from database client into Apex generic errors
	// This is a helper function for extended repository that implement extra functionalities
	// If you are using this function, you should implement the extended repository
	// and call this function inside the extended repository
	WrapError(ctx context.Context, err error) error
	// Unwanted functions
	GetDB() *gorm.DB
	HealthCheck() error
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

func (r *repository[T]) FindByField(ctx context.Context, field string, value interface{}) (*T, error) {
	var entity T
	err := r.db.WithContext(ctx).Where(field+" = ?", value).First(&entity).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, GenericError
	}

	return &entity, nil
}

func (r *repository[T]) FindByQuery(ctx context.Context, query string, args ...interface{}) (*T, error) {
	var entity T
	err := r.db.WithContext(ctx).Where(query, args...).First(&entity).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, GenericError
	}

	return &entity, nil
}

func (r *repository[T]) Create(ctx context.Context, entity *T) (*T, error) {
	err := r.db.WithContext(ctx).Create(&entity).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if err != nil {
		return nil, GenericError
	}

	return entity, nil
}

// Deprecated: Use FindOne instead.
func (r *repository[T]) FindByID(ctx context.Context, ID string) (*T, error) {
	var entity T
	err := r.db.WithContext(ctx).Model(&entity).Where("id = ?", ID).First(&entity).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NotFoundError
	} else if err != nil {
		return nil, GenericError
	}

	return &entity, nil
}

func (r *repository[T]) FindAll(ctx context.Context) ([]*T, error) {
	var entities []*T
	err := r.db.WithContext(ctx).Find(&entities).Error
	r.handleDBError(ctx, err)

	if err != nil {
		return nil, GenericError
	}

	return entities, nil
}

func (r *repository[T]) Update(ctx context.Context, entity *T, fields T) (*T, error) {
	err := r.db.WithContext(ctx).Model(entity).Updates(fields).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if err != nil {
		return nil, GenericError
	}

	return entity, nil
}

func (r *repository[T]) UpdateAllWhere(
	ctx context.Context,
	where map[string]interface{},
	updates map[string]interface{},
) (int64, error) {
	var model T
	result := r.db.WithContext(ctx).
		Model(&model).
		Where(where).
		Updates(updates)

	if result.Error != nil {
		return 0, r.WrapError(ctx, result.Error)
	}

	return result.RowsAffected, nil
}

func (r *repository[T]) DeleteByID(ctx context.Context, ID string) error {
	var entity T
	err := r.db.WithContext(ctx).Where("id = ?", ID).Delete(&entity).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return handledErr
	}

	if err != nil {
		return GenericError
	}

	return nil
}

func (r *repository[T]) DeleteWhere(ctx context.Context, where map[string]interface{}) error {
	var entity T
	err := r.db.WithContext(ctx).Where(where).Delete(&entity).Error

	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return handledErr
	}

	if err != nil {
		return GenericError
	}

	return nil
}

func (r *repository[T]) HardDeleteByID(ctx context.Context, ID string) error {
	var entity T
	err := r.db.WithContext(ctx).Unscoped().Where("id = ?", ID).Delete(&entity).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return handledErr
	}

	if err != nil {
		return GenericError
	}

	return nil
}

// Update and return updated entity
func (r *repository[T]) UpdateByID(ctx context.Context, ID string, fields T) (*T, error) {
	var entity T
	err := r.db.WithContext(ctx).Model(&entity).Clauses(clause.Returning{}).Where("id = ?", ID).Updates(fields).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if err != nil {
		return nil, GenericError
	}

	return &entity, nil
}

func (r *repository[T]) UpdateByIDWithMaps(ctx context.Context, ID string, fields map[string]interface{}) (*T, error) {
	var entity T

	// Use the map[string]interface{} to update the record
	err := r.db.WithContext(ctx).
		Model(&entity).
		Clauses(clause.Returning{}). // To return the updated entity
		Where("id = ?", ID).
		Updates(fields).
		Error

	// Handle database errors
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if err != nil {
		return nil, GenericError
	}

	return &entity, nil
}

func (r *repository[T]) FindAllWhere(ctx context.Context, query string, args ...interface{}) ([]*T, error) {
	var entities []*T
	err := r.db.WithContext(ctx).Where(query, args...).Find(&entities).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return nil, handledErr
	}

	if err != nil {
		return nil, GenericError
	}

	return entities, nil
}

func (r *repository[T]) handleDBError(ctx context.Context, err error) error {
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
			return DuplicateError
		case ForeignKeyViolation:
			return RelationNotExist
		default:
			return nil
		}

	}
	return nil
}

func (r *repository[T]) WrapError(ctx context.Context, err error) error {
	// If error is wrapped, return wrapped error
	handledErr := r.handleDBError(ctx, err)
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
		return GenericError
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

	// if options.Query != nil {
	// 	db.Where(options.Query.Clause, options.Query.Args...)
	// }

	if options.Where != nil {
		db = db.Where(map[string]interface{}(options.Where))
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
						tx = tx.Where(map[string]interface{}(filter))
					}
				}
				return tx.Select([]string(cFields))
			})
		}
	}

	return db
}

func (r *repository[T]) FindOne(ctx context.Context, options FindOptions) (*T, error) {
	var result *T
	db := r.db.WithContext(ctx).Model(result)
	db = r.applyFindOptionsToDB(db, options)

	if err := db.First(&result).Error; err != nil {
		err = r.WrapError(ctx, err)
		return nil, err
	}
	return result, nil
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
		db = db.Where(map[string]interface{}(options.Where))
	}

	err := db.Count(&count).Error
	if err != nil {
		return 0, r.WrapError(ctx, err)
	}

	return count, nil
}

func (r *repository[T]) Transaction(ctx context.Context, fn func(txRepo Repository[T]) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := NewRepository[T](tx)
		return fn(txRepo)
	})
}

func (r *repository[T]) GetDB() *gorm.DB {
	return r.db
}

func (r *repository[T]) BatchCreate(ctx context.Context, entities []*T, batchSize uint) error {
	if len(entities) == 0 {
		return nil
	}

	// If batchSize is not provided (zero), use a default value
	if batchSize == 0 {
		batchSize = 100
	}

	err := r.db.WithContext(ctx).CreateInBatches(&entities, int(batchSize)).Error
	handledErr := r.handleDBError(ctx, err)
	if handledErr != nil {
		return handledErr
	}

	if err != nil {
		return GenericError
	}

	return nil
}
