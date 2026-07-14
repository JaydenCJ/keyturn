// Tests for key generation, parsing, hashing, and redaction — the
// format is a public contract, so these lock its shape down.
package key

import (
	"errors"
	"strings"
	"testing"
)

// seqReader is a deterministic randomness source: bytes 0,1,2,3,…
// Keys generated from it are stable across runs, which is what lets
// higher-level tests assert on exact key strings.
type seqReader struct{ n byte }

func (r *seqReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.n
		r.n++
	}
	return len(p), nil
}

func TestGenerateProducesParseableKey(t *testing.T) {
	k, err := Generate("", &seqReader{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	p, err := Parse(k.Full)
	if err != nil {
		t.Fatalf("Parse(%q): %v", k.Full, err)
	}
	if p.ID != k.ID {
		t.Errorf("parsed ID %q, want %q", p.ID, k.ID)
	}
	if p.Label != "" {
		t.Errorf("parsed label %q, want empty", p.Label)
	}
}

func TestGenerateWithLabelEmbedsLabelSegment(t *testing.T) {
	k, err := Generate("live", &seqReader{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(k.Full, "kt_live_") {
		t.Errorf("key %q should start with kt_live_", k.Full)
	}
	p, err := Parse(k.Full)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Label != "live" {
		t.Errorf("parsed label %q, want live", p.Label)
	}
}

func TestGenerateIsDeterministicAndAlphabetBound(t *testing.T) {
	a, _ := Generate("", &seqReader{})
	b, _ := Generate("", &seqReader{})
	if a.Full != b.Full {
		t.Errorf("same reader state must give same key: %q vs %q", a.Full, b.Full)
	}
	k, _ := Generate("", &seqReader{n: 200})
	body := strings.TrimPrefix(k.Full, "kt_")
	for _, r := range strings.ReplaceAll(body, "_", "") {
		if !strings.ContainsRune(alphabet, r) {
			t.Fatalf("key %q contains %q outside the alphabet", k.Full, r)
		}
	}
}

func TestGenerateRejectsBadLabel(t *testing.T) {
	for _, label := range []string{"UPPER", "has space", "under_score", "waaaaaaaaaaaaaaaytoolong"} {
		if _, err := Generate(label, &seqReader{}); err == nil {
			t.Errorf("label %q should be rejected", label)
		}
	}
}

func TestParseRejectsMalformedShapes(t *testing.T) {
	long := strings.Repeat("a", SecretLen)
	id := strings.Repeat("b", IDLen)
	cases := map[string]string{
		"empty":              "",
		"no prefix":          "sk_" + id + "_" + long,
		"two segments":       "kt_" + long,
		"five segments":      "kt_a_b_" + id + "_" + long,
		"short id":           "kt_abc_" + long,
		"short secret":       "kt_" + id + "_tooshort",
		"bad secret charset": "kt_" + id + "_" + strings.Repeat("L", SecretLen),
		"bad id charset":     "kt_" + strings.Repeat("!", IDLen) + "_" + long,
		"empty label":        "kt__" + id + "_" + long,
	}
	for name, s := range cases {
		if _, err := Parse(s); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: Parse(%q) = %v, want ErrMalformed", name, s, err)
		}
	}
}

func TestParseAcceptsLabelWithFullLowercaseRange(t *testing.T) {
	// "live" contains letters outside the random-segment alphabet
	// (i, l) — labels are human-chosen and must allow them.
	full := "kt_live_" + strings.Repeat("b", IDLen) + "_" + strings.Repeat("c", SecretLen)
	p, err := Parse(full)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Label != "live" {
		t.Errorf("label = %q, want live", p.Label)
	}
}

func TestMatchesAcceptsOnlyTheExactKey(t *testing.T) {
	if h := Hash("kt_x"); h != Hash("kt_x") || len(h) != 64 {
		t.Error("Hash must be a stable 64-char hex digest")
	}
	k, _ := Generate("", &seqReader{})
	if !Matches(k.Hash, k.Full) {
		t.Errorf("key must match its own hash")
	}
	if Matches(k.Hash, k.Full+"x") {
		t.Errorf("altered key must not match")
	}
	other, _ := Generate("", &seqReader{n: 99})
	if Matches(k.Hash, other.Full) {
		t.Errorf("different key must not match")
	}
}

func TestRedactDropsTheSecret(t *testing.T) {
	k, _ := Generate("live", &seqReader{})
	red := Redact(k.Full)
	if strings.Contains(red, k.Full[len(k.Full)-SecretLen:]) {
		t.Fatalf("redacted form %q leaks the secret", red)
	}
	if !strings.Contains(red, k.ID) {
		t.Errorf("redacted form %q should keep the ID", red)
	}
	if got := Redact("not a key at all"); got != "kt_…" {
		t.Errorf("Redact(garbage) = %q, want kt_…", got)
	}
}

func TestValidateLabelBoundaries(t *testing.T) {
	if err := ValidateLabel(""); err != nil {
		t.Errorf("empty label must be allowed: %v", err)
	}
	if err := ValidateLabel(strings.Repeat("a", 16)); err != nil {
		t.Errorf("16-char label must be allowed: %v", err)
	}
	if err := ValidateLabel(strings.Repeat("a", 17)); err == nil {
		t.Errorf("17-char label must be rejected")
	}
}
