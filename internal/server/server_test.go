// Tests for the HTTP surface, run in-process against httptest — the
// exact handlers the sidecar serves, with a pinned clock and
// deterministic key generation.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaydenCJ/keyturn/internal/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const adminToken = "test-admin-token"

type seqReader struct{ n byte }

func (r *seqReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.n
		r.n++
	}
	return len(p), nil
}

// newServer builds a Server on a fresh temp store with a mutable clock.
func newServer(t *testing.T) (*Server, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := t0
	return &Server{
		Store:      st,
		AdminToken: adminToken,
		Now:        func() time.Time { return now },
		Rand:       &seqReader{},
	}, &now
}

// do performs a request against the handler and decodes the JSON body.
func do(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: body is not JSON: %v\n%s", method, path, err, rr.Body.String())
	}
	return rr.Code, out
}

// mintViaAPI creates a key through the admin API and returns it.
func mintViaAPI(t *testing.T, h http.Handler, body string) (string, string) {
	t.Helper()
	code, out := do(t, h, "POST", "/v1/keys", adminToken, body)
	if code != http.StatusCreated {
		t.Fatalf("create: status %d: %v", code, out)
	}
	rec := out["record"].(map[string]any)
	return out["key"].(string), rec["id"].(string)
}

func TestHealthz(t *testing.T) {
	s, _ := newServer(t)
	code, out := do(t, s.Handler(), "GET", "/healthz", "", "")
	if code != 200 || out["ok"] != true {
		t.Fatalf("healthz: %d %v", code, out)
	}
	if out["version"] != "0.1.0" {
		t.Errorf("version = %v, want 0.1.0", out["version"])
	}
}

func TestVerifyValidKey(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	k, id := mintViaAPI(t, h, `{"name":"acme","scopes":["read:*"],"rate":"5/1m"}`)
	code, out := do(t, h, "POST", "/v1/verify", "",
		fmt.Sprintf(`{"key":%q,"scopes":["read:users"]}`, k))
	if code != 200 || out["valid"] != true {
		t.Fatalf("verify: %d %v", code, out)
	}
	if out["key_id"] != id || out["name"] != "acme" {
		t.Errorf("identity fields wrong: %v", out)
	}
	if out["remaining"].(float64) != 4 {
		t.Errorf("remaining = %v, want 4", out["remaining"])
	}
}

func TestVerifyDeniedIsStill200(t *testing.T) {
	// Transport success vs authorization outcome must stay separable:
	// the proxy retries 5xx, never a definitive "invalid key".
	s, _ := newServer(t)
	h := s.Handler()
	code, out := do(t, h, "POST", "/v1/verify", "",
		`{"key":"kt_zzzzzzzzzz_zzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200 for a definitive denial", code)
	}
	if out["valid"] != false || out["reason"] != "not_found" {
		t.Errorf("out = %v, want valid=false reason=not_found", out)
	}
}

func TestVerifyRateLimitedReportsRetryAfterAndRefills(t *testing.T) {
	s, now := newServer(t)
	h := s.Handler()
	k, _ := mintViaAPI(t, h, `{"name":"acme","rate":"1/1m"}`)
	body := fmt.Sprintf(`{"key":%q}`, k)
	do(t, h, "POST", "/v1/verify", "", body)
	code, out := do(t, h, "POST", "/v1/verify", "", body)
	if code != 200 || out["reason"] != "rate_limited" {
		t.Fatalf("second call: %d %v", code, out)
	}
	if out["retry_after_ms"].(float64) != 60000 {
		t.Errorf("retry_after_ms = %v, want 60000", out["retry_after_ms"])
	}
	// The bucket refills with the injected clock, not the wall clock.
	*now = t0.Add(time.Minute)
	code, out = do(t, h, "POST", "/v1/verify", "", body)
	if code != 200 || out["valid"] != true {
		t.Fatalf("after a full window: %d %v", code, out)
	}
}

func TestVerifyRejectsBadRequests(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	for name, body := range map[string]string{
		"not json":      `{{{`,
		"missing key":   `{"scopes":["a"]}`,
		"unknown field": `{"key":"kt_x","token":"y"}`,
	} {
		code, _ := do(t, h, "POST", "/v1/verify", "", body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, code)
		}
	}
	// Oversized bodies are cut off with 413 before they parse.
	big := `{"key":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	if code, _ := do(t, h, "POST", "/v1/verify", "", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status = %d, want 413", code)
	}
}

func TestAdminRequiresTheRightToken(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	if code, _ := do(t, h, "GET", "/v1/keys", "", ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", code)
	}
	if code, _ := do(t, h, "GET", "/v1/keys", "wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d, want 401", code)
	}
	if code, _ := do(t, h, "GET", "/v1/keys", adminToken, ""); code != 200 {
		t.Errorf("right token: %d, want 200", code)
	}
}

func TestAdminDisabledWithoutConfiguredToken(t *testing.T) {
	s, _ := newServer(t)
	s.AdminToken = ""
	code, out := do(t, s.Handler(), "GET", "/v1/keys", "anything", "")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when no admin token is configured", code)
	}
	if !strings.Contains(out["error"].(string), "disabled") {
		t.Errorf("error should say the admin API is disabled: %v", out)
	}
}

func TestCreateReturnsTheKeyExactlyOnce(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	k, id := mintViaAPI(t, h, `{"name":"acme","label":"live","scopes":["read:*"]}`)
	if !strings.HasPrefix(k, "kt_live_") {
		t.Errorf("key %q should embed the label", k)
	}
	// Neither list nor get may ever contain the key or its hash.
	_, out := do(t, h, "GET", "/v1/keys/"+id, adminToken, "")
	blob, _ := json.Marshal(out)
	if strings.Contains(string(blob), k) || strings.Contains(string(blob), "hash") {
		t.Errorf("record view leaks secrets: %s", blob)
	}
}

func TestCreatePersistsToDisk(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	k, _ := mintViaAPI(t, h, `{"name":"acme"}`)
	// A brand-new store process must be able to verify the key.
	st2, err := store.Open(s.Store.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2 := &Server{Store: st2, Now: func() time.Time { return t0 }}
	code, out := do(t, s2.Handler(), "POST", "/v1/verify", "", fmt.Sprintf(`{"key":%q}`, k))
	if code != 200 || out["valid"] != true {
		t.Fatalf("fresh process verify: %d %v", code, out)
	}
}

func TestCreateRejectsBadParams(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	for name, body := range map[string]string{
		"no name":     `{}`,
		"bad scope":   `{"name":"x","scopes":["NOPE"]}`,
		"bad rate":    `{"name":"x","rate":"lots"}`,
		"past expiry": `{"name":"x","expires":"2020-01-01"}`,
	} {
		code, _ := do(t, h, "POST", "/v1/keys", adminToken, body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, code)
		}
	}
}

func TestListShowsAllKeysAndGetUnknownIs404(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	mintViaAPI(t, h, `{"name":"one"}`)
	mintViaAPI(t, h, `{"name":"two"}`)
	_, out := do(t, h, "GET", "/v1/keys", adminToken, "")
	keys := out["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("list has %d keys, want 2", len(keys))
	}
	if code, _ := do(t, h, "GET", "/v1/keys/zzzzzzzzzz", adminToken, ""); code != http.StatusNotFound {
		t.Errorf("get unknown id: status = %d, want 404", code)
	}
}

func TestRevokeAndEnableRoundTrip(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	k, id := mintViaAPI(t, h, `{"name":"acme"}`)
	verifyBody := fmt.Sprintf(`{"key":%q}`, k)

	code, out := do(t, h, "POST", "/v1/keys/"+id+"/revoke", adminToken, "")
	if code != 200 || out["disabled"] != true {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if _, out := do(t, h, "POST", "/v1/verify", "", verifyBody); out["reason"] != "disabled" {
		t.Errorf("revoked key should verify as disabled: %v", out)
	}

	code, out = do(t, h, "POST", "/v1/keys/"+id+"/enable", adminToken, "")
	if code != 200 || out["disabled"] != false {
		t.Fatalf("enable: %d %v", code, out)
	}
	if _, out := do(t, h, "POST", "/v1/verify", "", verifyBody); out["valid"] != true {
		t.Errorf("re-enabled key should verify: %v", out)
	}
}

func TestDeleteRemovesTheKey(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	k, id := mintViaAPI(t, h, `{"name":"acme"}`)
	code, out := do(t, h, "DELETE", "/v1/keys/"+id, adminToken, "")
	if code != 200 || out["deleted"] != id {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, out := do(t, h, "POST", "/v1/verify", "", fmt.Sprintf(`{"key":%q}`, k)); out["reason"] != "not_found" {
		t.Errorf("deleted key should be not_found: %v", out)
	}
	if code, _ := do(t, h, "DELETE", "/v1/keys/"+id, adminToken, ""); code != http.StatusNotFound {
		t.Errorf("double delete: %d, want 404", code)
	}
}

func TestWrongMethodIs405(t *testing.T) {
	s, _ := newServer(t)
	req := httptest.NewRequest("GET", "/v1/verify", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/verify: %d, want 405", rr.Code)
	}
}
