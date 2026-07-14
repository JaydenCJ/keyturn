// Tests for the verification pipeline: every rung of the ladder, the
// order of the rungs, and the information each outcome may reveal.
package verify

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JaydenCJ/keyturn/internal/issue"
	"github.com/JaydenCJ/keyturn/internal/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type seqReader struct{ n byte }

func (r *seqReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.n
		r.n++
	}
	return len(p), nil
}

// mint issues a key straight into a fresh store and returns both.
func mint(t *testing.T, p issue.Params) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	issued, err := issue.Issue(p, t0, &seqReader{})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	st.Put(issued.Record)
	return st, issued.Key.Full
}

func TestValidUnlimitedKey(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "ci-bot", Meta: map[string]string{"team": "sre"}})
	res := Check(st, Request{Key: k}, t0)
	if !res.Valid || res.Reason != ReasonNone {
		t.Fatalf("res = %+v, want valid", res)
	}
	if res.Name != "ci-bot" || res.Meta["team"] != "sre" {
		t.Errorf("result must carry key identity: %+v", res)
	}
	if res.Remaining != -1 {
		t.Errorf("unlimited key remaining = %d, want -1", res.Remaining)
	}
}

func TestMalformedKeyString(t *testing.T) {
	st, _ := mint(t, issue.Params{Name: "x"})
	res := Check(st, Request{Key: "Bearer whatever"}, t0)
	if res.Valid || res.Reason != ReasonMalformed {
		t.Fatalf("res = %+v, want malformed", res)
	}
	if res.KeyID != "" {
		t.Error("malformed result must not name a key")
	}
}

func TestUnknownIDIsNotFound(t *testing.T) {
	st, _ := mint(t, issue.Params{Name: "x"})
	ghost := "kt_zzzzzzzzzz_zzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	res := Check(st, Request{Key: ghost}, t0)
	if res.Valid || res.Reason != ReasonNotFound {
		t.Fatalf("res = %+v, want not_found", res)
	}
}

func TestWrongSecretGetsTheSameAnswerAsUnknownID(t *testing.T) {
	// If wrong-secret and unknown-id were distinguishable, an attacker
	// could confirm which IDs exist. Both must read as not_found, and
	// neither may leak the key's name.
	st, k := mint(t, issue.Params{Name: "x"})
	tampered := k[:len(k)-4] + "0000"
	res := Check(st, Request{Key: tampered}, t0)
	if res.Valid || res.Reason != ReasonNotFound {
		t.Fatalf("res = %+v, want not_found", res)
	}
	if res.KeyID != "" || res.Name != "" {
		t.Error("wrong-secret result must not identify the record")
	}
}

func TestDisabledKey(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x"})
	res := Check(st, Request{Key: k}, t0)
	id := res.KeyID
	if err := st.SetDisabled(id, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	res = Check(st, Request{Key: k}, t0)
	if res.Valid || res.Reason != ReasonDisabled {
		t.Fatalf("res = %+v, want disabled", res)
	}
	if res.KeyID != id {
		t.Error("a disabled key is still identified (the caller holds the secret)")
	}
}

func TestExpiredKeyAndTheExactBoundary(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x", Expires: "2026-02-01T00:00:00Z"})
	expiry := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if res := Check(st, Request{Key: k}, expiry.Add(-time.Nanosecond)); !res.Valid {
		t.Errorf("one ns before expiry must be valid: %+v", res)
	}
	// At the expiry instant itself the key is dead — expiry is exclusive.
	if res := Check(st, Request{Key: k}, expiry); res.Valid || res.Reason != ReasonExpired {
		t.Errorf("at the expiry instant: %+v, want expired", res)
	}
}

func TestMissingScopeListsExactlyWhatIsMissing(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x", Scopes: []string{"read:*"}})
	res := Check(st, Request{Key: k, Scopes: []string{"read:users", "write:users", "admin"}}, t0)
	if res.Valid || res.Reason != ReasonMissingScope {
		t.Fatalf("res = %+v, want missing_scope", res)
	}
	if len(res.MissingScopes) != 2 || res.MissingScopes[0] != "write:users" || res.MissingScopes[1] != "admin" {
		t.Errorf("MissingScopes = %v, want [write:users admin]", res.MissingScopes)
	}
}

func TestWildcardAndScopelessGrants(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x", Scopes: []string{"read:*"}})
	res := Check(st, Request{Key: k, Scopes: []string{"read:users:42"}}, t0)
	if !res.Valid {
		t.Fatalf("read:* must grant read:users:42: %+v", res)
	}
	// A request demanding no scopes only needs a valid key.
	st2, k2 := mint(t, issue.Params{Name: "y"}) // key with no scopes at all
	if res := Check(st2, Request{Key: k2}, t0); !res.Valid {
		t.Fatalf("scopeless request against scopeless key must pass: %+v", res)
	}
}

func TestRateLimitSpendsAndDenies(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x", Rate: "2/1m"})
	if res := Check(st, Request{Key: k}, t0); !res.Valid || res.Remaining != 1 {
		t.Fatalf("call 1: %+v, want valid remaining 1", res)
	}
	if res := Check(st, Request{Key: k}, t0); !res.Valid || res.Remaining != 0 {
		t.Fatalf("call 2: %+v, want valid remaining 0", res)
	}
	res := Check(st, Request{Key: k}, t0)
	if res.Valid || res.Reason != ReasonRateLimited {
		t.Fatalf("call 3: %+v, want rate_limited", res)
	}
	if res.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s (1 token at 2/1m)", res.RetryAfter)
	}
	// The bucket refills with the clock: a call at t0+30s passes again.
	if res := Check(st, Request{Key: k}, t0.Add(30*time.Second)); !res.Valid {
		t.Errorf("after the retry hint the call must pass: %+v", res)
	}
}

func TestRateLimitStatePersistsThroughTheStore(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x", Rate: "5/1m"})
	Check(st, Request{Key: k}, t0)
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st2, err := store.Open(st.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	res := Check(st2, Request{Key: k}, t0)
	if res.Remaining != 3 {
		t.Errorf("remaining after reload = %d, want 3 (one spent before save)", res.Remaining)
	}
}

func TestCostSpendsMultipleTokens(t *testing.T) {
	st, k := mint(t, issue.Params{Name: "x", Rate: "10/1m"})
	res := Check(st, Request{Key: k, Cost: 7}, t0)
	if !res.Valid || res.Remaining != 3 {
		t.Fatalf("cost 7: %+v, want remaining 3", res)
	}
	res = Check(st, Request{Key: k, Cost: 7}, t0)
	if res.Valid || res.Reason != ReasonRateLimited {
		t.Fatalf("second cost 7: %+v, want rate_limited", res)
	}
	// Cost above burst can never be satisfied: no finite retry hint.
	res = Check(st, Request{Key: k, Cost: 11}, t0)
	if res.Valid || res.RetryAfter >= 0 {
		t.Errorf("cost 11 vs burst 10: %+v, want denial with negative RetryAfter", res)
	}
}

func TestDeniedScopeDoesNotSpendTokens(t *testing.T) {
	// Rung order matters: a request that fails authorization must not
	// drain the caller's budget.
	st, k := mint(t, issue.Params{Name: "x", Scopes: []string{"read:*"}, Rate: "2/1m"})
	for i := 0; i < 5; i++ {
		Check(st, Request{Key: k, Scopes: []string{"admin"}}, t0)
	}
	res := Check(st, Request{Key: k, Scopes: []string{"read:users"}}, t0)
	if !res.Valid || res.Remaining != 1 {
		t.Fatalf("res = %+v — missing_scope checks must not have spent tokens", res)
	}
}

func TestRevokedBeatsExpiredBeatsScopes(t *testing.T) {
	// A disabled and expired key with missing scopes reports "disabled":
	// the ladder answers with the first failure, deterministically.
	st, k := mint(t, issue.Params{Name: "x", Scopes: []string{"a"}, Expires: "2026-01-10"})
	res := Check(st, Request{Key: k}, t0)
	if err := st.SetDisabled(res.KeyID, true); err != nil {
		t.Fatal(err)
	}
	res = Check(st, Request{Key: k, Scopes: []string{"zzz"}}, t0.Add(365*24*time.Hour))
	if res.Reason != ReasonDisabled {
		t.Errorf("reason = %q, want disabled to win", res.Reason)
	}
}
