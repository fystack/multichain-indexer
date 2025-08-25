package kvstore

// KVStore is an interface for a simple key-value store.
type KVStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
	Close() error
}
