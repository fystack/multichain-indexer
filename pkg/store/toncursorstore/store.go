package toncursorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/pkg/infra"
)

const keyPrefix = "ton/cursor/"

// AccountCursor tracks the polling position for a single TON account.
type AccountCursor struct {
	Address   string    `json:"address"`
	LastLT    uint64    `json:"last_lt"`   // Logical time of last processed tx
	LastHash  string    `json:"last_hash"` // Hex-encoded hash of last processed tx
	UpdatedAt time.Time `json:"updated_at"`
}

type Store interface {
	Get(ctx context.Context, address string) (*AccountCursor, error)
	Save(ctx context.Context, cursor *AccountCursor) error
	Delete(ctx context.Context, address string) error
	List(ctx context.Context) ([]string, error)
}

type kvStore struct {
	kv infra.KVStore
}

func New(kv infra.KVStore) Store {
	return &kvStore{kv: kv}
}

func (s *kvStore) cursorKey(address string) string {
	return keyPrefix + address
}

func (s *kvStore) Get(_ context.Context, address string) (*AccountCursor, error) {
	var cursor AccountCursor
	found, err := s.kv.GetAny(s.cursorKey(address), &cursor)
	if err != nil {
		return nil, fmt.Errorf("failed to get cursor for %s: %w", address, err)
	}
	if !found {
		return nil, nil
	}
	return &cursor, nil
}

func (s *kvStore) Save(_ context.Context, cursor *AccountCursor) error {
	cursor.UpdatedAt = time.Now()
	if err := s.kv.SetAny(s.cursorKey(cursor.Address), cursor); err != nil {
		return fmt.Errorf("failed to save cursor for %s: %w", cursor.Address, err)
	}
	return nil
}

func (s *kvStore) Delete(_ context.Context, address string) error {
	if err := s.kv.Delete(s.cursorKey(address)); err != nil {
		return fmt.Errorf("failed to delete cursor for %s: %w", address, err)
	}
	return nil
}

func (s *kvStore) List(_ context.Context) ([]string, error) {
	pairs, err := s.kv.List(keyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list cursors: %w", err)
	}

	addresses := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		var cursor AccountCursor
		if err := json.Unmarshal(pair.Value, &cursor); err != nil {
			continue
		}
		addresses = append(addresses, cursor.Address)
	}

	return addresses, nil
}
