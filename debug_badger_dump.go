package main

import (
	"fmt"
	"log"
	"os"

	"github.com/dgraph-io/badger/v4"
)

func main() {
	dbPath := "data/badger"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	// Open BadgerDB with read-only mode to avoid conflicts
	opts := badger.DefaultOptions(dbPath).
		WithLogger(nil).
		WithReadOnly(true)

	db, err := badger.Open(opts)
	if err != nil {
		log.Fatalf("Failed to open BadgerDB at %s: %v", dbPath, err)
	}
	defer db.Close()

	fmt.Printf("=== BadgerDB Contents (%s) ===\n\n", dbPath)

	keyCount := 0
	err = db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchSize = 10
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()
			
			err := item.Value(func(val []byte) error {
				keyCount++
				fmt.Printf("Key:   %s\n", string(key))
				fmt.Printf("Value: %s\n", string(val))
				fmt.Printf("Size:  %d bytes\n", len(val))
				fmt.Println("---")
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		log.Fatalf("Error iterating through database: %v", err)
	}

	fmt.Printf("\nTotal keys found: %d\n", keyCount)
}