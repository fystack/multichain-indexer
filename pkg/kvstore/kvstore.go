package kvstore

import "errors"

var (
	ErrKeyNotFound = errors.New("key not found")
	ErrKeyEmpty    = errors.New("key is empty")
)

// checkKeyAndValue returns an error if k == "" or if v == nil.
func checkKeyAndValue(k string, v any) error {
	if k == "" {
		return ErrKeyEmpty
	}
	if v == nil {
		return errors.New("the passed value is nil, which is not allowed")
	}
	return nil
}
