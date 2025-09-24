package kvstore

import (
	"fmt"

	"github.com/dgraph-io/badger/v4"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/hashicorp/consul/api"
)

type BadgerStore struct {
	db     *badger.DB
	prefix string
	codec  infra.Codec
}

func NewBadgerStore(path string, prefix string, codec infra.Codec) (*BadgerStore, error) {
	opts := badger.DefaultOptions(path).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &BadgerStore{
		db:     db,
		prefix: prefix,
		codec:  codec,
	}, nil
}

func (b *BadgerStore) fullKey(k string) (string, error) {
	if k == "" {
		return "", ErrKeyEmpty
	}
	if b.prefix != "" {
		return b.prefix + "/" + k, nil
	}
	return k, nil
}

func (b *BadgerStore) GetName() string {
	return string(enum.KVStoreTypeBadger)
}

func (b *BadgerStore) Get(key string) (string, error) {
	k, err := b.fullKey(key)
	if err != nil {
		return "", err
	}

	var valCopy []byte
	err = b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(k))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return ErrKeyNotFound
			}
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		valCopy = val
		return nil
	})
	return string(valCopy), err
}

func (b *BadgerStore) Set(key string, value string) error {
	k, err := b.fullKey(key)
	if err != nil {
		return err
	}

	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(k), []byte(value))
	})
}

// GetWithOptions is provided for interface parity; options are ignored for Badger.
func (b *BadgerStore) GetWithOptions(key string, _ *api.QueryOptions) (string, error) {
	return b.Get(key)
}

func (b *BadgerStore) SetAny(key string, value any) error {
	if err := checkKeyAndValue(key, value); err != nil {
		return err
	}
	k, err := b.fullKey(key)
	if err != nil {
		return err
	}

	data, err := b.codec.Marshal(value)
	if err != nil {
		return err
	}

	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(k), data)
	})
}

func (b *BadgerStore) GetAny(key string, value any) (bool, error) {
	if err := checkKeyAndValue(key, value); err != nil {
		return false, err
	}
	k, err := b.fullKey(key)
	if err != nil {
		return false, err
	}

	var valCopy []byte
	err = b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(k))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return ErrKeyNotFound
			}
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		valCopy = val
		return nil
	})
	if err != nil {
		if err == ErrKeyNotFound {
			return false, nil
		}
		return false, err
	}
	return true, b.codec.Unmarshal(valCopy, value)
}

func (b *BadgerStore) List(prefix string) ([]*infra.KVPair, error) {
	if prefix == "" {
		return nil, fmt.Errorf("prefix is empty")
	}
	searchPrefix := prefix
	if b.prefix != "" {
		searchPrefix = b.prefix + "/" + prefix
	}

	result := make([]*infra.KVPair, 0)
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		p := []byte(searchPrefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			result = append(result, &infra.KVPair{
				Key:   string(k),
				Value: v,
			})
		}
		return nil
	})
	return result, err
}

func (b *BadgerStore) Delete(key string) error {
	k, err := b.fullKey(key)
	if err != nil {
		return err
	}

	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(k))
	})
}

func (b *BadgerStore) Close() error {
	return b.db.Close()
}
