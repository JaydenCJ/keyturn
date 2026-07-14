// Package issue mints new keys: it validates the requested parameters,
// generates the key material, and produces the store record. Both the
// CLI and the admin HTTP API funnel through Issue so a key created
// either way is byte-for-byte the same shape.
package issue

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JaydenCJ/keyturn/internal/key"
	"github.com/JaydenCJ/keyturn/internal/rate"
	"github.com/JaydenCJ/keyturn/internal/scope"
	"github.com/JaydenCJ/keyturn/internal/store"
)

// maxNameLen bounds the human-readable key name.
const maxNameLen = 80

// Params describe the key to mint. Rate and Burst arrive as strings
// straight from a flag or JSON body; Issue parses and validates them.
type Params struct {
	Name    string            // required, ≤80 chars
	Label   string            // optional key-string label segment
	Scopes  []string          // granted scopes
	Rate    string            // "100/1m" syntax; "" = unlimited
	Burst   float64           // 0 = same as the rate's request count
	Expires string            // RFC 3339 or YYYY-MM-DD; "" = never
	Meta    map[string]string // free-form annotations
}

// Issued pairs the one-time-visible key with its persisted record.
type Issued struct {
	Key    key.Key
	Record store.Record
}

// Issue validates params and mints a key at instant now. rnd may be nil
// to use crypto/rand; tests inject a deterministic reader.
func Issue(p Params, now time.Time, rnd io.Reader) (Issued, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return Issued{}, fmt.Errorf("name is required")
	}
	if len(name) > maxNameLen {
		return Issued{}, fmt.Errorf("name longer than %d chars", maxNameLen)
	}
	if err := scope.ValidateAll(p.Scopes); err != nil {
		return Issued{}, err
	}
	limit, err := rate.ParseLimit(p.Rate)
	if err != nil {
		return Issued{}, err
	}
	if p.Burst != 0 {
		if limit.IsZero() {
			return Issued{}, fmt.Errorf("burst needs a rate limit to apply to")
		}
		if p.Burst < 1 {
			return Issued{}, fmt.Errorf("burst must be at least 1")
		}
		limit.Burst = p.Burst
	}
	expires, err := parseExpiry(p.Expires, now)
	if err != nil {
		return Issued{}, err
	}
	if err := validateMeta(p.Meta); err != nil {
		return Issued{}, err
	}
	k, err := key.Generate(p.Label, rnd)
	if err != nil {
		return Issued{}, err
	}
	rec := store.Record{
		ID:        k.ID,
		Name:      name,
		Label:     k.Label,
		Hash:      k.Hash,
		Scopes:    append([]string(nil), p.Scopes...),
		Limit:     limit,
		CreatedAt: now.UTC(),
		ExpiresAt: expires,
		Meta:      p.Meta,
	}
	return Issued{Key: k, Record: rec}, nil
}

// parseExpiry accepts RFC 3339 ("2027-01-02T15:04:05Z") or a bare date
// ("2027-01-02", meaning midnight UTC) and requires it to be in the
// future — issuing an already-expired key is always a mistake.
func parseExpiry(s string, now time.Time) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02", s)
		if err != nil {
			return nil, fmt.Errorf("expiry %q: want RFC 3339 or YYYY-MM-DD", s)
		}
	}
	t = t.UTC()
	if !t.After(now) {
		return nil, fmt.Errorf("expiry %s is not in the future", s)
	}
	return &t, nil
}

func validateMeta(meta map[string]string) error {
	if len(meta) > 32 {
		return fmt.Errorf("too many meta entries (max 32)")
	}
	for k, v := range meta {
		if k == "" {
			return fmt.Errorf("meta key is empty")
		}
		if len(k) > 64 || len(v) > 256 {
			return fmt.Errorf("meta %q: key ≤64 and value ≤256 chars", k)
		}
	}
	return nil
}
