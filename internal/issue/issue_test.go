// Tests for key issuance: parameter validation and the shape of the
// record that lands in the store.
package issue

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// seqReader mirrors the deterministic randomness used in key tests.
type seqReader struct{ n byte }

func (r *seqReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.n
		r.n++
	}
	return len(p), nil
}

func TestIssueBuildsAConsistentRecord(t *testing.T) {
	got, err := Issue(Params{
		Name:   "ci-bot",
		Label:  "live",
		Scopes: []string{"read:*"},
		Rate:   "100/1m",
		Meta:   map[string]string{"team": "platform"},
	}, t0, &seqReader{})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := got.Record
	if r.ID != got.Key.ID {
		t.Errorf("record ID %q != key ID %q", r.ID, got.Key.ID)
	}
	if r.Hash != got.Key.Hash {
		t.Errorf("record hash must equal the key hash")
	}
	if strings.Contains(r.Hash, got.Key.Full) {
		t.Error("record must never contain the full key")
	}
	if r.Limit.Requests != 100 || r.Limit.Window != time.Minute || r.Limit.Burst != 100 {
		t.Errorf("limit = %+v, want 100/1m", r.Limit)
	}
	if !r.CreatedAt.Equal(t0) {
		t.Errorf("CreatedAt = %v, want the injected clock", r.CreatedAt)
	}
	if r.Label != "live" || r.Meta["team"] != "platform" {
		t.Errorf("label/meta not carried: %+v", r)
	}
}

func TestIssueRequiresTrimsAndBoundsTheName(t *testing.T) {
	if _, err := Issue(Params{}, t0, &seqReader{}); err == nil {
		t.Error("empty name must be rejected")
	}
	if _, err := Issue(Params{Name: "   "}, t0, &seqReader{}); err == nil {
		t.Error("whitespace-only name must be rejected")
	}
	got, err := Issue(Params{Name: "  ci-bot  "}, t0, &seqReader{})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got.Record.Name != "ci-bot" {
		t.Errorf("name = %q, want trimmed", got.Record.Name)
	}
	if _, err := Issue(Params{Name: strings.Repeat("x", 81)}, t0, &seqReader{}); err == nil {
		t.Error("81-char name must be rejected")
	}
}

func TestIssueRejectsInvalidScopesAndRates(t *testing.T) {
	if _, err := Issue(Params{Name: "x", Scopes: []string{"Bad Scope"}}, t0, &seqReader{}); err == nil {
		t.Error("invalid scope must be rejected")
	}
	if _, err := Issue(Params{Name: "x", Scopes: []string{"a", "a"}}, t0, &seqReader{}); err == nil {
		t.Error("duplicate scopes must be rejected")
	}
	if _, err := Issue(Params{Name: "x", Rate: "lots"}, t0, &seqReader{}); err == nil {
		t.Error("unparseable rate must be rejected")
	}
}

func TestIssueBurstOverridesDefault(t *testing.T) {
	got, err := Issue(Params{Name: "x", Rate: "10/1m", Burst: 50}, t0, &seqReader{})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got.Record.Limit.Burst != 50 {
		t.Errorf("burst = %v, want 50", got.Record.Limit.Burst)
	}
}

func TestIssueBurstWithoutRateIsAnError(t *testing.T) {
	if _, err := Issue(Params{Name: "x", Burst: 50}, t0, &seqReader{}); err == nil {
		t.Error("burst without a rate must be rejected")
	}
	if _, err := Issue(Params{Name: "x", Rate: "10/1m", Burst: 0.5}, t0, &seqReader{}); err == nil {
		t.Error("burst below 1 must be rejected")
	}
}

func TestIssueExpiryAcceptsBothForms(t *testing.T) {
	got, err := Issue(Params{Name: "x", Expires: "2027-06-01T12:00:00Z"}, t0, &seqReader{})
	if err != nil {
		t.Fatalf("RFC 3339 expiry: %v", err)
	}
	if got.Record.ExpiresAt == nil || got.Record.ExpiresAt.Hour() != 12 {
		t.Errorf("expiry = %v, want 2027-06-01T12:00:00Z", got.Record.ExpiresAt)
	}
	got, err = Issue(Params{Name: "x", Expires: "2027-06-01"}, t0, &seqReader{})
	if err != nil {
		t.Fatalf("date expiry: %v", err)
	}
	if got.Record.ExpiresAt == nil || !got.Record.ExpiresAt.Equal(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date expiry = %v, want midnight UTC", got.Record.ExpiresAt)
	}
}

func TestIssueRejectsPastOrGarbageExpiry(t *testing.T) {
	if _, err := Issue(Params{Name: "x", Expires: "2020-01-01"}, t0, &seqReader{}); err == nil {
		t.Error("past expiry must be rejected")
	}
	if _, err := Issue(Params{Name: "x", Expires: "soon"}, t0, &seqReader{}); err == nil {
		t.Error("garbage expiry must be rejected")
	}
}

func TestIssueBoundsMetadata(t *testing.T) {
	big := map[string]string{}
	for i := 0; i < 33; i++ {
		big[strings.Repeat("k", i+1)] = "v"
	}
	if _, err := Issue(Params{Name: "x", Meta: big}, t0, &seqReader{}); err == nil {
		t.Error("33 meta entries must be rejected")
	}
	if _, err := Issue(Params{Name: "x", Meta: map[string]string{"k": strings.Repeat("v", 257)}}, t0, &seqReader{}); err == nil {
		t.Error("oversized meta value must be rejected")
	}
	if _, err := Issue(Params{Name: "x", Meta: map[string]string{"": "v"}}, t0, &seqReader{}); err == nil {
		t.Error("empty meta key must be rejected")
	}
}

func TestIssueIsDeterministicWithSeededReader(t *testing.T) {
	a, _ := Issue(Params{Name: "x"}, t0, &seqReader{})
	b, _ := Issue(Params{Name: "x"}, t0, &seqReader{})
	if a.Key.Full != b.Key.Full {
		t.Error("same reader state must mint the same key")
	}
}
