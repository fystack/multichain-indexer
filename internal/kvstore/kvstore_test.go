package kvstore

import (
	"os"
	"testing"
)

func TestBadgerStore_BasicOperations(t *testing.T) {
	// Create a temporary directory for the test database
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a new BadgerStore
	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	// Test Set and Get
	key := "test_key"
	value := []byte("test_value")

	err = store.Set(key, value)
	if err != nil {
		t.Errorf("Failed to set key: %v", err)
	}

	retrieved, err := store.Get(key)
	if err != nil {
		t.Errorf("Failed to get key: %v", err)
	}

	if string(retrieved) != string(value) {
		t.Errorf("Expected value %s, got %s", string(value), string(retrieved))
	}
}

func TestBadgerStore_GetNonExistentKey(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	// Try to get a non-existent key
	_, err = store.Get("non_existent_key")
	if err == nil {
		t.Error("Expected error when getting non-existent key, got nil")
	}
}

func TestBadgerStore_Delete(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	key := "test_key"
	value := []byte("test_value")

	// Set a key
	err = store.Set(key, value)
	if err != nil {
		t.Errorf("Failed to set key: %v", err)
	}

	// Verify it exists
	retrieved, err := store.Get(key)
	if err != nil {
		t.Errorf("Failed to get key: %v", err)
	}
	if string(retrieved) != string(value) {
		t.Errorf("Expected value %s, got %s", string(value), string(retrieved))
	}

	// Delete the key
	err = store.Delete(key)
	if err != nil {
		t.Errorf("Failed to delete key: %v", err)
	}

	// Verify it's gone
	_, err = store.Get(key)
	if err == nil {
		t.Error("Expected error when getting deleted key, got nil")
	}
}

func TestBadgerStore_EmptyValue(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	key := "empty_key"
	value := []byte{}

	// Set empty value
	err = store.Set(key, value)
	if err != nil {
		t.Errorf("Failed to set empty value: %v", err)
	}

	// Retrieve empty value
	retrieved, err := store.Get(key)
	if err != nil {
		t.Errorf("Failed to get empty value: %v", err)
	}

	if len(retrieved) != 0 {
		t.Errorf("Expected empty value, got %v", retrieved)
	}
}

func TestBadgerStore_LargeValue(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	key := "large_key"
	// Create a large value (1MB)
	value := make([]byte, 1024*1024)
	for i := range value {
		value[i] = byte(i % 256)
	}

	// Set large value
	err = store.Set(key, value)
	if err != nil {
		t.Errorf("Failed to set large value: %v", err)
	}

	// Retrieve large value
	retrieved, err := store.Get(key)
	if err != nil {
		t.Errorf("Failed to get large value: %v", err)
	}

	if len(retrieved) != len(value) {
		t.Errorf("Expected value length %d, got %d", len(value), len(retrieved))
	}

	// Verify content
	for i := range value {
		if retrieved[i] != value[i] {
			t.Errorf("Value mismatch at index %d", i)
			break
		}
	}
}

func TestBadgerStore_MultipleOperations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}
	defer store.Close()

	// Test multiple operations
	testData := map[string][]byte{
		"key1": []byte("value1"),
		"key2": []byte("value2"),
		"key3": []byte("value3"),
	}

	// Set all values
	for key, value := range testData {
		err = store.Set(key, value)
		if err != nil {
			t.Errorf("Failed to set %s: %v", key, err)
		}
	}

	// Get all values
	for key, expectedValue := range testData {
		retrieved, err := store.Get(key)
		if err != nil {
			t.Errorf("Failed to get %s: %v", key, err)
		}
		if string(retrieved) != string(expectedValue) {
			t.Errorf("Expected %s=%s, got %s", key, string(expectedValue), string(retrieved))
		}
	}

	// Delete one key
	err = store.Delete("key2")
	if err != nil {
		t.Errorf("Failed to delete key2: %v", err)
	}

	// Verify key2 is gone
	_, err = store.Get("key2")
	if err == nil {
		t.Error("Expected error when getting deleted key2, got nil")
	}

	// Verify other keys still exist
	for key, expectedValue := range testData {
		if key == "key2" {
			continue
		}
		retrieved, err := store.Get(key)
		if err != nil {
			t.Errorf("Failed to get %s: %v", key, err)
		}
		if string(retrieved) != string(expectedValue) {
			t.Errorf("Expected %s=%s, got %s", key, string(expectedValue), string(retrieved))
		}
	}
}

func TestBadgerStore_InvalidPath(t *testing.T) {
	// Try to create BadgerStore with invalid path
	_, err := NewBadgerStore("/invalid/path/that/does/not/exist")
	if err == nil {
		t.Error("Expected error when creating BadgerStore with invalid path, got nil")
	}
}

func TestBadgerStore_Close(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "badger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := NewBadgerStore(tempDir)
	if err != nil {
		t.Fatalf("Failed to create BadgerStore: %v", err)
	}

	// Close the store
	err = store.Close()
	if err != nil {
		t.Errorf("Failed to close store: %v", err)
	}

	// Try to use the store after closing (should not panic)
	err = store.Set("key", []byte("value"))
	if err == nil {
		t.Error("Expected error when using closed store, got nil")
	}
}
