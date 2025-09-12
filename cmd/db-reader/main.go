package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

type DBReader struct {
	db *badger.DB
}

func NewDBReader(dbPath string) (*DBReader, error) {
	opts := badger.DefaultOptions(dbPath)
	opts.ReadOnly = true // Open in read-only mode

	// Try to open with read-only mode first
	db, err := badger.Open(opts)
	if err != nil {
		// If that fails, try without read-only mode (might be locked by another process)
		opts.ReadOnly = false
		db, err = badger.Open(opts)
		if err != nil {
			return nil, fmt.Errorf("failed to open badger db: %w", err)
		}
	}

	return &DBReader{db: db}, nil
}

func (r *DBReader) Close() error {
	return r.db.Close()
}

// ListAllKeys lists all keys in the database
func (r *DBReader) ListAllKeys() error {
	return r.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false // We only need keys

		it := txn.NewIterator(opts)
		defer it.Close()

		var keys []string
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())
			keys = append(keys, key)
		}

		sort.Strings(keys)

		fmt.Printf("Found %d keys in database:\n", len(keys))
		for i, key := range keys {
			fmt.Printf("%3d. %s\n", i+1, key)
		}

		return nil
	})
}

// GetValue retrieves and displays a specific key's value
func (r *DBReader) GetValue(key string) error {
	return r.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return fmt.Errorf("key not found: %s", key)
		}

		value, err := item.ValueCopy(nil)
		if err != nil {
			return fmt.Errorf("failed to copy value: %w", err)
		}

		fmt.Printf("Key: %s\n", key)
		fmt.Printf("Value (hex): %x\n", value)
		fmt.Printf("Value (string): %s\n", string(value))
		fmt.Printf("Size: %d bytes\n", len(value))

		// Try to parse as JSON if it looks like JSON
		if len(value) > 0 && (value[0] == '{' || value[0] == '[') {
			var prettyJSON interface{}
			if err := json.Unmarshal(value, &prettyJSON); err == nil {
				prettyBytes, _ := json.MarshalIndent(prettyJSON, "", "  ")
				fmt.Printf("Value (JSON):\n%s\n", string(prettyBytes))
			}
		}

		return nil
	})
}

// SearchKeys searches for keys matching a pattern
func (r *DBReader) SearchKeys(pattern string) error {
	return r.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false

		it := txn.NewIterator(opts)
		defer it.Close()

		var matches []string
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())
			if strings.Contains(key, pattern) {
				matches = append(matches, key)
			}
		}

		sort.Strings(matches)

		if len(matches) == 0 {
			fmt.Printf("No keys found matching pattern: %s\n", pattern)
			return nil
		}

		fmt.Printf("Found %d keys matching '%s':\n", len(matches), pattern)
		for i, key := range matches {
			fmt.Printf("%3d. %s\n", i+1, key)
		}

		return nil
	})
}

// ListCatchupProgress lists all catchup progress entries
func (r *DBReader) ListCatchupProgress() error {
	return r.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true

		it := txn.NewIterator(opts)
		defer it.Close()

		var progressEntries []struct {
			Key   string
			Value string
		}

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())

			if strings.HasPrefix(key, "catchup_progress_") {
				value, err := item.ValueCopy(nil)
				if err != nil {
					continue
				}

				progressEntries = append(progressEntries, struct {
					Key   string
					Value string
				}{
					Key:   key,
					Value: string(value),
				})
			}
		}

		sort.Slice(progressEntries, func(i, j int) bool {
			return progressEntries[i].Key < progressEntries[j].Key
		})

		if len(progressEntries) == 0 {
			fmt.Println("No catchup progress entries found")
			return nil
		}

		fmt.Printf("Found %d catchup progress entries:\n", len(progressEntries))
		for i, entry := range progressEntries {
			fmt.Printf("%3d. %s = %s\n", i+1, entry.Key, entry.Value)
		}

		return nil
	})
}

// ListLatestBlocks lists all latest block entries
func (r *DBReader) ListLatestBlocks() error {
	return r.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true

		it := txn.NewIterator(opts)
		defer it.Close()

		var blockEntries []struct {
			Key   string
			Value string
		}

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())

			if strings.HasPrefix(key, "latest_block_") {
				value, err := item.ValueCopy(nil)
				if err != nil {
					continue
				}

				blockEntries = append(blockEntries, struct {
					Key   string
					Value string
				}{
					Key:   key,
					Value: string(value),
				})
			}
		}

		sort.Slice(blockEntries, func(i, j int) bool {
			return blockEntries[i].Key < blockEntries[j].Key
		})

		if len(blockEntries) == 0 {
			fmt.Println("No latest block entries found")
			return nil
		}

		fmt.Printf("Found %d latest block entries:\n", len(blockEntries))
		for i, entry := range blockEntries {
			fmt.Printf("%3d. %s = %s\n", i+1, entry.Key, entry.Value)
		}

		return nil
	})
}

func main() {
	var (
		dbPath   = flag.String("db", "", "Path to Badger database directory")
		listKeys = flag.Bool("list", false, "List all keys")
		getKey   = flag.String("get", "", "Get value for specific key")
		search   = flag.String("search", "", "Search keys containing pattern")
		catchup  = flag.Bool("catchup", false, "List catchup progress entries")
		latest   = flag.Bool("latest", false, "List latest block entries")
	)
	flag.Parse()

	if *dbPath == "" {
		// Try to find the default database path
		defaultPaths := []string{
			"data/badger",
		}

		for _, path := range defaultPaths {
			if _, err := os.Stat(path); err == nil {
				*dbPath = path
				break
			}
		}

		if *dbPath == "" {
			log.Fatal(
				"Database path not specified. Use -db flag or ensure 'data' directory exists.",
			)
		}
	}

	// Ensure the path exists
	if _, err := os.Stat(*dbPath); os.IsNotExist(err) {
		log.Fatalf("Database path does not exist: %s", *dbPath)
	}

	reader, err := NewDBReader(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer reader.Close()

	fmt.Printf("Opened Badger database: %s\n\n", *dbPath)

	// Execute requested operations
	if *listKeys {
		if err := reader.ListAllKeys(); err != nil {
			log.Printf("Error listing keys: %v", err)
		}
	}

	if *getKey != "" {
		if err := reader.GetValue(*getKey); err != nil {
			log.Printf("Error getting value: %v", err)
		}
	}

	if *search != "" {
		if err := reader.SearchKeys(*search); err != nil {
			log.Printf("Error searching keys: %v", err)
		}
	}

	if *catchup {
		if err := reader.ListCatchupProgress(); err != nil {
			log.Printf("Error listing catchup progress: %v", err)
		}
	}

	if *latest {
		if err := reader.ListLatestBlocks(); err != nil {
			log.Printf("Error listing latest blocks: %v", err)
		}
	}

	// If no specific operation requested, show help
	if !*listKeys && *getKey == "" && *search == "" && !*catchup && !*latest {
		fmt.Println("Usage examples:")
		fmt.Println("  ./db-reader -list                    # List all keys")
		fmt.Println("  ./db-reader -catchup                 # List catchup progress")
		fmt.Println("  ./db-reader -latest                  # List latest blocks")
		fmt.Println("  ./db-reader -search catchup          # Search for keys containing 'catchup'")
		fmt.Println("  ./db-reader -get 'latest_block_evm'  # Get specific key value")
		fmt.Println("  ./db-reader -db /path/to/db -list    # Specify custom database path")
	}
}
