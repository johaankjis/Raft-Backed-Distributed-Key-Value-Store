package storage

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v3"
)

// Storage wraps a BadgerDB instance for persistent key-value storage
// In production, this would use RocksDB, but BadgerDB is easier to embed in Go
type Storage struct {
	db *badger.DB
	mu sync.RWMutex
}

// NewStorage creates a new storage instance
func NewStorage(dataDir string) (*Storage, error) {
	opts := badger.DefaultOptions(dataDir)
	opts.Logger = nil // Disable badger logging for cleaner output
	
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return &Storage{db: db}, nil
}

// Put stores a key-value pair
func (s *Storage) Put(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	})
}

// Get retrieves a value by key
func (s *Storage) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var value []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		value, err = item.ValueCopy(nil)
		return err
	})

	if err == badger.ErrKeyNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	return value, true, nil
}

// Delete removes a key-value pair
func (s *Storage) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// GetSnapshot returns a snapshot of all data
func (s *Storage) GetSnapshot() (map[string][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key())
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			snapshot[key] = val
		}
		return nil
	})

	return snapshot, err
}

// RestoreSnapshot restores data from a snapshot
func (s *Storage) RestoreSnapshot(snapshot map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing data
	err := s.db.DropAll()
	if err != nil {
		return err
	}

	// Restore snapshot data
	return s.db.Update(func(txn *badger.Txn) error {
		for key, value := range snapshot {
			if err := txn.Set([]byte(key), value); err != nil {
				return err
			}
		}
		return nil
	})
}

// Close closes the database
func (s *Storage) Close() error {
	return s.db.Close()
}

// RaftLog stores Raft log entries and metadata
type RaftLog struct {
	db *badger.DB
	mu sync.RWMutex
}

// NewRaftLog creates a new Raft log storage
func NewRaftLog(dataDir string) (*RaftLog, error) {
	opts := badger.DefaultOptions(dataDir + "/raft-log")
	opts.Logger = nil
	
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open raft log: %w", err)
	}

	return &RaftLog{db: db}, nil
}

// LogEntry represents a Raft log entry
type LogEntry struct {
	Index       uint64 `json:"index"`
	Term        uint64 `json:"term"`
	Command     []byte `json:"command"`
	CommandType string `json:"command_type"`
}

// AppendEntry appends a log entry
func (rl *RaftLog) AppendEntry(entry LogEntry) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	return rl.db.Update(func(txn *badger.Txn) error {
		key := fmt.Sprintf("log:%d", entry.Index)
		return txn.Set([]byte(key), data)
	})
}

// GetEntry retrieves a log entry by index
func (rl *RaftLog) GetEntry(index uint64) (*LogEntry, error) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var entry LogEntry
	err := rl.db.View(func(txn *badger.Txn) error {
		key := fmt.Sprintf("log:%d", index)
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		data, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &entry)
	})

	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	return &entry, err
}

// GetLastIndex returns the index of the last log entry
func (rl *RaftLog) GetLastIndex() (uint64, error) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var lastIndex uint64
	err := rl.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		opts.PrefetchValues = false
		
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte("log:")
		for it.Seek(append(prefix, 0xFF)); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := string(item.Key())
			fmt.Sscanf(key, "log:%d", &lastIndex)
			break
		}
		return nil
	})

	return lastIndex, err
}

// SetMetadata stores Raft metadata (currentTerm, votedFor)
func (rl *RaftLog) SetMetadata(key string, value uint64) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return rl.db.Update(func(txn *badger.Txn) error {
		data, _ := json.Marshal(value)
		return txn.Set([]byte("meta:"+key), data)
	})
}

// GetMetadata retrieves Raft metadata
func (rl *RaftLog) GetMetadata(key string) (uint64, error) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var value uint64
	err := rl.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("meta:" + key))
		if err != nil {
			return err
		}
		data, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &value)
	})

	if err == badger.ErrKeyNotFound {
		return 0, nil
	}
	return value, err
}

// Close closes the Raft log database
func (rl *RaftLog) Close() error {
	return rl.db.Close()
}
