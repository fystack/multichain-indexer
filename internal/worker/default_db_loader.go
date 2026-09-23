package worker

import (
	"context"
	"errors"

	"github.com/fystack/multichain-indexer/pkg/model"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// DefaultDBLoader loads addresses from a wallet_addresses table.
// This is a reference implementation of AddressLoader; replace it with
// your own loader if your schema differs.
type DefaultDBLoader struct {
	db *gorm.DB
}

func NewDefaultDBLoader(db *gorm.DB) *DefaultDBLoader {
	return &DefaultDBLoader{db: db}
}

func (l *DefaultDBLoader) LoadAddresses(ctx context.Context, params AddressLoaderParams) ([]LoadedAddress, error) {
	var rows []model.WalletAddress
	err := l.db.WithContext(ctx).
		Model(&model.WalletAddress{}).
		Select("address", "created_at").
		Where("type = ? AND created_at > ?", params.NetworkType, params.After).
		Order("created_at ASC").
		Limit(params.Limit).
		Find(&rows).Error
	if err != nil {
		// Filtering by an enum value the DB type does not define (22P02): no rows.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, nil
		}
		return nil, err
	}

	addresses := make([]LoadedAddress, len(rows))
	for i, r := range rows {
		addresses[i] = LoadedAddress{
			Address:   r.Address,
			CreatedAt: r.CreatedAt,
		}
	}
	return addresses, nil
}
