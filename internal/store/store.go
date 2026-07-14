// Package store persists key records in a single JSON file.
//
// The file is the whole database: human-readable, diffable, and easy to
// back up. Writes go through a temp file + rename so a crash can never
// leave a half-written store, and a mutex serializes all access so the
// server and its handlers share one Store safely. Secrets never touch
// the file — records hold only the SHA-256 hash of the full key.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/JaydenCJ/keyturn/internal/rate"
)

// SchemaVersion identifies the on-disk format.
const SchemaVersion = 1

// ErrNotFound is returned when a key ID has no record.
var ErrNotFound = errors.New("key not found")

// Record is everything keyturn knows about one key.
type Record struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Label     string            `json:"label,omitempty"`
	Hash      string            `json:"hash"`
	Scopes    []string          `json:"scopes,omitempty"`
	Limit     rate.Limit        `json:"limit"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Bucket    rate.State        `json:"bucket"`
}

// Clone returns a deep copy so callers can mutate freely.
func (r Record) Clone() Record {
	c := r
	c.Scopes = append([]string(nil), r.Scopes...)
	if r.ExpiresAt != nil {
		t := *r.ExpiresAt
		c.ExpiresAt = &t
	}
	if r.Meta != nil {
		c.Meta = make(map[string]string, len(r.Meta))
		for k, v := range r.Meta {
			c.Meta[k] = v
		}
	}
	return c
}

// file is the serialized shape of the store.
type file struct {
	Schema int      `json:"schema_version"`
	Keys   []Record `json:"keys"`
}

// Store is an in-memory map of records bound to a JSON file path.
type Store struct {
	mu   sync.Mutex
	path string
	keys map[string]Record
}

// Open loads the store at path, creating an empty store (in memory
// only — the file appears on first Save) when the file does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, keys: make(map[string]Record)}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("store %s is not valid JSON: %w", path, err)
	}
	if f.Schema != SchemaVersion {
		return nil, fmt.Errorf("store %s has schema %d; this build reads %d",
			path, f.Schema, SchemaVersion)
	}
	for _, r := range f.Keys {
		if r.ID == "" || r.Hash == "" {
			return nil, fmt.Errorf("store %s: record missing id or hash", path)
		}
		if _, dup := s.keys[r.ID]; dup {
			return nil, fmt.Errorf("store %s: duplicate key id %q", path, r.ID)
		}
		s.keys[r.ID] = r
	}
	return s, nil
}

// Path returns the file path this store persists to.
func (s *Store) Path() string { return s.path }

// Save writes the store atomically: marshal → temp file in the same
// directory → fsync-free rename. Records are sorted by creation time
// (ID as tiebreak) so saves are byte-stable for identical content.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	f := file{Schema: SchemaVersion, Keys: s.sortedLocked()}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding store: %w", err)
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".keyturn-*.tmp")
	if err != nil {
		return fmt.Errorf("writing store: %w", err)
	}
	tmpName := tmp.Name()
	// 0600: the store holds credential hashes and metadata; keep it private.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing store: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("writing store: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("writing store: %w", err)
	}
	return nil
}

// Put inserts or replaces a record.
func (s *Store) Put(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[r.ID] = r.Clone()
}

// Get returns a copy of the record for id.
func (s *Store) Get(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.keys[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r.Clone(), nil
}

// Delete removes the record for id.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(s.keys, id)
	return nil
}

// List returns all records, oldest first (ID as tiebreak).
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sortedLocked()
	for i := range out {
		out[i] = out[i].Clone()
	}
	return out
}

// Len reports the number of records.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

// SetDisabled flips the disabled flag on a record.
func (s *Store) SetDisabled(id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.keys[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	r.Disabled = disabled
	s.keys[id] = r
	return nil
}

// Update applies fn to the record for id under the store lock. fn gets
// a copy; the returned record is stored. This is how verification
// commits token-bucket state without racing concurrent requests.
func (s *Store) Update(id string, fn func(Record) Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.keys[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	s.keys[id] = fn(r.Clone())
	return nil
}

func (s *Store) sortedLocked() []Record {
	out := make([]Record, 0, len(s.keys))
	for _, r := range s.keys {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
