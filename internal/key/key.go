// Package key implements keyturn's key material: generation, parsing,
// and hashing.
//
// A full key looks like
//
//	kt_live_8b0kdmvyq2_c3vjkm5rw8xz0q4t7ye2ahsgn6pd
//	└┬┘ └┬─┘ └───┬────┘ └────────────┬─────────────┘
//	 │   │       │                   └ secret (28 chars, random)
//	 │   │       └ key ID (10 chars, random, safe to log)
//	 │   └ optional label ("live", "test", a tenant name …)
//	 └ fixed product prefix
//
// Only the SHA-256 hash of the full key string is ever stored; the ID is
// the lookup handle and is not secret. The character set is Crockford
// base32 (lowercase, no i/l/o/u) so keys survive copy-paste, URLs, and
// human eyes.
package key

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// Prefix is the fixed leading token of every keyturn key.
	Prefix = "kt"
	// IDLen is the length of the public key ID segment.
	IDLen = 10
	// SecretLen is the length of the secret segment.
	SecretLen = 28
)

// alphabet is Crockford base32, lowercase: unambiguous and URL-safe.
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// ErrMalformed is returned when a presented string is not a keyturn key.
var ErrMalformed = errors.New("malformed key")

// Key is a freshly generated key. Secret material lives only in Full;
// callers must show Full exactly once and persist only ID + Hash.
type Key struct {
	Full  string // the complete key string, shown once at creation
	ID    string // public lookup handle, safe to log
	Label string // optional label segment ("" if none)
	Hash  string // hex SHA-256 of Full — the only stored credential
}

// Generate mints a new key with an optional label. The label may be
// empty; otherwise it must be 1-16 chars from the key alphabet.
func Generate(label string, rnd io.Reader) (Key, error) {
	if err := ValidateLabel(label); err != nil {
		return Key{}, err
	}
	id, err := randomString(IDLen, rnd)
	if err != nil {
		return Key{}, err
	}
	secret, err := randomString(SecretLen, rnd)
	if err != nil {
		return Key{}, err
	}
	parts := []string{Prefix}
	if label != "" {
		parts = append(parts, label)
	}
	parts = append(parts, id, secret)
	full := strings.Join(parts, "_")
	return Key{Full: full, ID: id, Label: label, Hash: Hash(full)}, nil
}

// ValidateLabel checks a key label: empty is allowed, otherwise 1-16
// lowercase alphanumerics. Labels are human-chosen ("live", "test", a
// tenant slug), so the full a-z range is allowed — only the random ID
// and secret segments are restricted to the unambiguous alphabet.
// Underscores are excluded because '_' is the key's segment separator.
func ValidateLabel(label string) error {
	if label == "" {
		return nil
	}
	if len(label) > 16 {
		return fmt.Errorf("label too long (max 16 chars): %q", label)
	}
	for _, r := range label {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return fmt.Errorf("label may only contain a-z and 0-9: %q", label)
		}
	}
	return nil
}

// Parsed is the outcome of splitting a presented key string.
type Parsed struct {
	ID    string
	Label string
}

// Parse splits a presented key and returns its public parts. It never
// allocates a copy of the secret. Errors wrap ErrMalformed so callers
// can map every parse failure to one public "malformed" reason.
func Parse(full string) (Parsed, error) {
	segs := strings.Split(full, "_")
	// kt_id_secret (3 segments) or kt_label_id_secret (4 segments).
	if len(segs) != 3 && len(segs) != 4 {
		return Parsed{}, fmt.Errorf("%w: want kt_[label_]id_secret", ErrMalformed)
	}
	if segs[0] != Prefix {
		return Parsed{}, fmt.Errorf("%w: missing %q prefix", ErrMalformed, Prefix)
	}
	label := ""
	if len(segs) == 4 {
		label = segs[1]
		if ValidateLabel(label) != nil || label == "" {
			return Parsed{}, fmt.Errorf("%w: bad label segment", ErrMalformed)
		}
	}
	id, secret := segs[len(segs)-2], segs[len(segs)-1]
	if len(id) != IDLen || !validSegment(id) {
		return Parsed{}, fmt.Errorf("%w: bad id segment", ErrMalformed)
	}
	if len(secret) != SecretLen || !validSegment(secret) {
		return Parsed{}, fmt.Errorf("%w: bad secret segment", ErrMalformed)
	}
	return Parsed{ID: id, Label: label}, nil
}

// Hash returns the hex SHA-256 digest of the full key string.
func Hash(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

// Matches reports whether a presented full key hashes to storedHash,
// in constant time over the digest comparison.
func Matches(storedHash, presented string) bool {
	got := Hash(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// Redact renders a key for logs: prefix, label, ID kept; secret dropped.
// Safe on arbitrary input — anything unparseable becomes "kt_…".
func Redact(full string) string {
	p, err := Parse(full)
	if err != nil {
		return Prefix + "_…"
	}
	if p.Label != "" {
		return fmt.Sprintf("%s_%s_%s_…", Prefix, p.Label, p.ID)
	}
	return fmt.Sprintf("%s_%s_…", Prefix, p.ID)
}

func validSegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

// randomString draws n chars from the key alphabet by masking random
// bytes to 5 bits; 32 divides 256, so the result is exactly uniform.
func randomString(n int, rnd io.Reader) (string, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := io.ReadFull(rnd, buf); err != nil {
			return "", fmt.Errorf("reading randomness: %w", err)
		}
		// 32 divides 256, so masking the low 5 bits is already uniform.
		out = append(out, alphabet[buf[0]&31])
	}
	return string(out), nil
}
