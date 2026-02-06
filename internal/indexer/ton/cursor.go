package ton

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/multichain-indexer/pkg/infra"
)

const cursorKeyPrefix = "ton/cursor/"

// AccountCursor tracks the polling position for a single TON account.
type AccountCursor struct {
	Address   string    `json:"address"`
	LastLT    uint64    `json:"last_lt"`   // Logical time of last processed tx
	LastHash  string    `json:"last_hash"` // Hex-encoded hash of last processed tx
	UpdatedAt time.Time `json:"updated_at"`
}

// CursorStore manages account cursors for TON polling.
type CursorStore interface {
	// Get returns the cursor for an account, or nil if not found.
	Get(ctx context.Context, address string) (*AccountCursor, error)

	// Save persists the cursor atomically.
	Save(ctx context.Context, cursor *AccountCursor) error

	// Delete removes the cursor for an account.
	Delete(ctx context.Context, address string) error

	// List returns all tracked account addresses.
	List(ctx context.Context) ([]string, error)
}

// kvCursorStore implements CursorStore using infra.KVStore.
type kvCursorStore struct {
	kv infra.KVStore
}

func NewCursorStore(kv infra.KVStore) CursorStore {
	return &kvCursorStore{kv: kv}
}

func (s *kvCursorStore) cursorKey(address string) string {
	return cursorKeyPrefix + address
}

func (s *kvCursorStore) Get(ctx context.Context, address string) (*AccountCursor, error) {
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

func (s *kvCursorStore) Save(ctx context.Context, cursor *AccountCursor) error {
	cursor.UpdatedAt = time.Now()
	if err := s.kv.SetAny(s.cursorKey(cursor.Address), cursor); err != nil {
		return fmt.Errorf("failed to save cursor for %s: %w", cursor.Address, err)
	}
	return nil
}

func (s *kvCursorStore) Delete(ctx context.Context, address string) error {
	if err := s.kv.Delete(s.cursorKey(address)); err != nil {
		return fmt.Errorf("failed to delete cursor for %s: %w", address, err)
	}
	return nil
}

func (s *kvCursorStore) List(ctx context.Context) ([]string, error) {
	pairs, err := s.kv.List(cursorKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list cursors: %w", err)
	}

	addresses := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		var cursor AccountCursor
		if err := json.Unmarshal(pair.Value, &cursor); err != nil {
			continue // Skip malformed entries
		}
		addresses = append(addresses, cursor.Address)
	}

	return addresses, nil
}
