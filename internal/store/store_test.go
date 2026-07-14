// Tests for the JSON file store: atomic persistence, schema guards,
// and copy semantics that keep callers from aliasing internal state.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/JaydenCJ/keyturn/internal/rate"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func rec(id string, created time.Time) Record {
	return Record{ID: id, Name: "key-" + id, Hash: "hash-" + id, CreatedAt: created}
}

func tempStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpenMissingFileGivesEmptyStore(t *testing.T) {
	s := tempStore(t)
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Error("Open must not create the file; only Save does")
	}
}

func TestSaveThenOpenRoundTrips(t *testing.T) {
	s := tempStore(t)
	exp := t0.Add(24 * time.Hour)
	r := rec("aaaaaaaaaa", t0)
	r.Scopes = []string{"read:*"}
	r.Limit = rate.Limit{Requests: 5, Window: time.Minute, Burst: 5}
	r.ExpiresAt = &exp
	r.Meta = map[string]string{"team": "platform"}
	r.Bucket = rate.State{Tokens: 2.5, Updated: t0.UnixNano()}
	s.Put(r)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2, err := Open(s.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.Get("aaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != r.Name || got.Scopes[0] != "read:*" || got.Limit != r.Limit ||
		!got.ExpiresAt.Equal(exp) || got.Meta["team"] != "platform" || got.Bucket != r.Bucket {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestSaveWritesPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	s := tempStore(t)
	s.Put(rec("aaaaaaaaaa", t0))
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store file mode %o, want 600 — it holds credential hashes", perm)
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path()))
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want just the store file (no temp litter)", len(entries))
	}
}

func TestSaveIsByteStableForIdenticalContent(t *testing.T) {
	s := tempStore(t)
	s.Put(rec("bbbbbbbbbb", t0.Add(time.Hour)))
	s.Put(rec("aaaaaaaaaa", t0))
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first, _ := os.ReadFile(s.Path())
	if err := s.Save(); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	second, _ := os.ReadFile(s.Path())
	if string(first) != string(second) {
		t.Error("two saves of identical content must be byte-identical")
	}
	var f struct {
		Schema int `json:"schema_version"`
	}
	if err := json.Unmarshal(second, &f); err != nil {
		t.Fatalf("store file is not JSON: %v", err)
	}
	if f.Schema != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", f.Schema, SchemaVersion)
	}
}

func TestOpenRejectsBadStoreFiles(t *testing.T) {
	// A store that cannot be trusted must fail loudly at load, never be
	// silently misread — every one of these shapes has a distinct trap.
	cases := map[string]string{
		"unknown schema": `{"schema_version": 99, "keys": []}`,
		"truncated JSON": `{"schema_version": 1, "keys": [`,
		"duplicate ids": `{"schema_version":1,"keys":[
	  {"id":"aaaaaaaaaa","name":"x","hash":"h","created_at":"2026-01-02T03:04:05Z","limit":{},"bucket":{}},
	  {"id":"aaaaaaaaaa","name":"y","hash":"h","created_at":"2026-01-02T03:04:05Z","limit":{},"bucket":{}}]}`,
		"record without hash": `{"schema_version":1,"keys":[{"id":"aaaaaaaaaa","name":"x","created_at":"2026-01-02T03:04:05Z","limit":{},"bucket":{}}]}`,
	}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "keys.json")
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := Open(path); err == nil {
			t.Errorf("%s: Open must fail", name)
		}
	}
}

func TestGetReturnsACopy(t *testing.T) {
	s := tempStore(t)
	r := rec("aaaaaaaaaa", t0)
	r.Scopes = []string{"read:users"}
	r.Meta = map[string]string{"k": "v"}
	s.Put(r)
	got, _ := s.Get("aaaaaaaaaa")
	got.Scopes[0] = "hacked"
	got.Meta["k"] = "hacked"
	again, _ := s.Get("aaaaaaaaaa")
	if again.Scopes[0] != "read:users" || again.Meta["k"] != "v" {
		t.Error("mutating a Get result must not touch the store")
	}
}

func TestDeleteAndNotFound(t *testing.T) {
	s := tempStore(t)
	s.Put(rec("aaaaaaaaaa", t0))
	if err := s.Delete("aaaaaaaaaa"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("aaaaaaaaaa"); err == nil {
		t.Error("second delete must report not found")
	}
	if _, err := s.Get("aaaaaaaaaa"); err == nil {
		t.Error("deleted record must not be gettable")
	}
}

func TestListIsSortedOldestFirst(t *testing.T) {
	s := tempStore(t)
	s.Put(rec("cccccccccc", t0.Add(2*time.Hour)))
	s.Put(rec("aaaaaaaaaa", t0))
	s.Put(rec("bbbbbbbbbb", t0.Add(time.Hour)))
	got := s.List()
	want := []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("List order %v, want %v", ids(got), want)
		}
	}
	// Equal timestamps tiebreak on ID so ordering stays deterministic.
	s2 := tempStore(t)
	s2.Put(rec("bbbbbbbbbb", t0))
	s2.Put(rec("aaaaaaaaaa", t0))
	if got := s2.List(); got[0].ID != "aaaaaaaaaa" {
		t.Errorf("equal timestamps must sort by ID, got %v", ids(got))
	}
}

func TestUpdateCommitsBucketState(t *testing.T) {
	s := tempStore(t)
	s.Put(rec("aaaaaaaaaa", t0))
	err := s.Update("aaaaaaaaaa", func(r Record) Record {
		r.Bucket = rate.State{Tokens: 3, Updated: t0.UnixNano()}
		return r
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get("aaaaaaaaaa")
	if got.Bucket.Tokens != 3 {
		t.Errorf("bucket tokens = %v, want 3", got.Bucket.Tokens)
	}
	if err := s.Update("zzzzzzzzzz", func(r Record) Record { return r }); err == nil {
		t.Error("Update on a missing id must fail")
	}
}

func TestSetDisabledFlipsTheFlag(t *testing.T) {
	s := tempStore(t)
	s.Put(rec("aaaaaaaaaa", t0))
	if err := s.SetDisabled("aaaaaaaaaa", true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	got, _ := s.Get("aaaaaaaaaa")
	if !got.Disabled {
		t.Error("record should be disabled")
	}
	_ = s.SetDisabled("aaaaaaaaaa", false)
	got, _ = s.Get("aaaaaaaaaa")
	if got.Disabled {
		t.Error("record should be re-enabled")
	}
	if err := s.SetDisabled("zzzzzzzzzz", true); err == nil {
		t.Error("SetDisabled on a missing id must fail")
	}
}

func ids(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}
