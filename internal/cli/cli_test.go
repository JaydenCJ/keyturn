// Tests for the CLI, run in-process through App with injected clock,
// randomness, environment, and buffers — the same code paths main()
// wires to the real world.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// fixture bundles an App with its captured output and store path.
type fixture struct {
	app    *App
	out    *bytes.Buffer
	err    *bytes.Buffer
	store  string
	now    time.Time
	getenv map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		out:    &bytes.Buffer{},
		err:    &bytes.Buffer{},
		store:  filepath.Join(t.TempDir(), "keys.json"),
		now:    t0,
		getenv: map[string]string{},
	}
	f.app = &App{
		Stdout: f.out,
		Stderr: f.err,
		Now:    func() time.Time { return f.now },
		Rand:   &seqReader{},
		Getenv: func(k string) string { return f.getenv[k] },
	}
	return f
}

// run executes a command with --store injected and returns the exit code.
func (f *fixture) run(args ...string) int {
	f.out.Reset()
	f.err.Reset()
	return f.app.Run(append(args, "--store", f.store))
}

// mustCreate mints a key and returns the full key string.
func (f *fixture) mustCreate(t *testing.T, args ...string) string {
	t.Helper()
	code := f.run(append([]string{"create", "--quiet"}, args...)...)
	if code != ExitOK {
		t.Fatalf("create exited %d: %s", code, f.err.String())
	}
	return strings.TrimSpace(f.out.String())
}

func TestVersionCommand(t *testing.T) {
	f := newFixture(t)
	if code := f.app.Run([]string{"version"}); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if got := f.out.String(); got != "keyturn 0.1.0\n" {
		t.Errorf("output %q, want keyturn 0.1.0", got)
	}
}

func TestNoArgsAndUnknownCommandExit2(t *testing.T) {
	f := newFixture(t)
	if code := f.app.Run(nil); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(f.err.String(), "Usage:") {
		t.Error("usage text should land on stderr")
	}
	f.err.Reset()
	if code := f.app.Run([]string{"frobnicate"}); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(f.err.String(), "frobnicate") {
		t.Error("error should name the unknown command")
	}
}

func TestCreateQuietAndTextOutputs(t *testing.T) {
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot", "--label", "live")
	if !strings.HasPrefix(k, "kt_live_") {
		t.Errorf("key %q should start with kt_live_", k)
	}
	if strings.Count(f.out.String(), "\n") != 1 {
		t.Errorf("quiet output should be exactly one line: %q", f.out.String())
	}
	// Text format shows the key once and warns on stderr.
	if code := f.run("create", "--name", "ci-bot"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, f.err.String())
	}
	if !strings.Contains(f.out.String(), "key:     kt_") {
		t.Errorf("text output should show the key: %s", f.out.String())
	}
	if !strings.Contains(f.err.String(), "cannot show it again") {
		t.Error("stderr should warn the key is shown once")
	}
}

func TestCreateJSONCarriesKeyAndRecord(t *testing.T) {
	f := newFixture(t)
	if code := f.run("create", "--name", "ci-bot", "--scopes", "read:*", "--rate", "5/1m", "--format", "json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, f.err.String())
	}
	var out struct {
		Key    string `json:"key"`
		Record struct {
			Name  string `json:"name"`
			Limit string `json:"limit"`
		} `json:"record"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &out); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if !strings.HasPrefix(out.Key, "kt_") || out.Record.Name != "ci-bot" || out.Record.Limit != "5/1m" {
		t.Errorf("json output wrong: %+v", out)
	}
}

func TestCreateRejectsBadParamsWithExit2(t *testing.T) {
	f := newFixture(t)
	if code := f.run("create"); code != ExitUsage {
		t.Fatalf("no name: exit %d, want %d", code, ExitUsage)
	}
	if code := f.run("create", "--name", "x", "--rate", "banana"); code != ExitUsage {
		t.Fatalf("bad rate: exit %d, want %d", code, ExitUsage)
	}
}

func TestVerifyValidKeyExits0(t *testing.T) {
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot", "--scopes", "read:metrics,logs:write")
	code := f.run("verify", k, "--scopes", "read:metrics")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, f.err.String())
	}
	if !strings.Contains(f.out.String(), "valid: ci-bot") {
		t.Errorf("output: %s", f.out.String())
	}
}

func TestVerifyMissingScopeExits1(t *testing.T) {
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot", "--scopes", "read:*")
	code := f.run("verify", k, "--scopes", "admin:all")
	if code != ExitDenied {
		t.Fatalf("exit %d, want %d", code, ExitDenied)
	}
	if !strings.Contains(f.out.String(), "denied: missing_scope") ||
		!strings.Contains(f.out.String(), "admin:all") {
		t.Errorf("output: %s", f.out.String())
	}
}

func TestVerifyMalformedKeyAndMissingArg(t *testing.T) {
	f := newFixture(t)
	f.mustCreate(t, "--name", "x")
	// A garbage key string is a definitive denial (exit 1)…
	if code := f.run("verify", "not-a-key"); code != ExitDenied {
		t.Fatalf("exit %d, want %d", code, ExitDenied)
	}
	if !strings.Contains(f.out.String(), "denied: malformed") {
		t.Errorf("output: %s", f.out.String())
	}
	// …while forgetting the argument entirely is a usage error (exit 2).
	if code := f.run("verify"); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
}

func TestVerifyRateLimitPersistsAcrossInvocations(t *testing.T) {
	// Offline gating only works if spent tokens land in the store file;
	// each run below is a fresh Store loaded from disk.
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot", "--rate", "2/1m")
	if code := f.run("verify", k); code != ExitOK {
		t.Fatalf("call 1 exit %d", code)
	}
	if code := f.run("verify", k); code != ExitOK {
		t.Fatalf("call 2 exit %d", code)
	}
	if code := f.run("verify", k); code != ExitDenied {
		t.Fatalf("call 3 exit %d, want %d", code, ExitDenied)
	}
	if !strings.Contains(f.out.String(), "denied: rate_limited") {
		t.Errorf("output: %s", f.out.String())
	}
	// Advance the injected clock a full window: allowed again.
	f.now = t0.Add(time.Minute)
	if code := f.run("verify", k); code != ExitOK {
		t.Fatalf("after refill exit %d: %s", code, f.out.String())
	}
	// JSON output carries the same wire shape as the HTTP endpoint.
	f.run("verify", k)
	if code := f.run("verify", k, "--format", "json"); code != ExitDenied {
		t.Fatalf("json denial exit %d, want denied", code)
	}
	var out struct {
		Valid        bool   `json:"valid"`
		Reason       string `json:"reason"`
		RetryAfterMS int64  `json:"retry_after_ms"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if out.Valid || out.Reason != "rate_limited" || out.RetryAfterMS != 30000 {
		t.Errorf("json = %+v", out)
	}
}

func TestListShowsKeysInATable(t *testing.T) {
	f := newFixture(t)
	if code := f.run("list"); code != ExitOK {
		t.Fatalf("empty list exit %d", code)
	}
	if !strings.Contains(f.out.String(), "keyturn create") {
		t.Errorf("empty store should suggest create: %s", f.out.String())
	}
	f.mustCreate(t, "--name", "alpha", "--scopes", "read:*", "--rate", "100/1m")
	f.mustCreate(t, "--name", "beta")
	if code := f.run("list"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	out := f.out.String()
	for _, want := range []string{"ID", "alpha", "beta", "100/1m", "unlimited", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestShowDisplaysTheRecord(t *testing.T) {
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot", "--meta", "team=platform", "--meta", "env=prod")
	id := keyID(t, k)
	if code := f.run("show", id); code != ExitOK {
		t.Fatalf("exit %d: %s", code, f.err.String())
	}
	out := f.out.String()
	for _, want := range []string{"name:      ci-bot", "team=platform", "env=prod", "status:    active"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, k) {
		t.Error("show must never print the full key")
	}
}

func TestRevokeEnableDeleteLifecycle(t *testing.T) {
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot")
	id := keyID(t, k)

	if code := f.run("revoke", id); code != ExitOK {
		t.Fatalf("revoke exit %d", code)
	}
	if code := f.run("verify", k); code != ExitDenied {
		t.Fatal("revoked key must be denied")
	}
	if code := f.run("enable", id); code != ExitOK {
		t.Fatalf("enable exit %d", code)
	}
	if code := f.run("verify", k); code != ExitOK {
		t.Fatal("re-enabled key must verify")
	}
	if code := f.run("delete", id); code != ExitOK {
		t.Fatalf("delete exit %d", code)
	}
	if code := f.run("show", id); code != ExitRuntime {
		t.Fatalf("show after delete exit %d, want %d", code, ExitRuntime)
	}
}

func TestExpiredKeyShowsAsExpired(t *testing.T) {
	f := newFixture(t)
	k := f.mustCreate(t, "--name", "ci-bot", "--expires", "2026-01-03")
	f.now = t0.Add(48 * time.Hour)
	if code := f.run("verify", k); code != ExitDenied {
		t.Fatal("expired key must be denied")
	}
	if !strings.Contains(f.out.String(), "denied: expired") {
		t.Errorf("output: %s", f.out.String())
	}
	f.run("list")
	if !strings.Contains(f.out.String(), "expired") {
		t.Errorf("list should flag expired keys: %s", f.out.String())
	}
}

func TestStorePathFallsBackToEnvironment(t *testing.T) {
	f := newFixture(t)
	f.getenv["KEYTURN_STORE"] = filepath.Join(t.TempDir(), "env-store.json")
	if code := f.app.Run([]string{"create", "--name", "x", "--quiet"}); code != ExitOK {
		t.Fatalf("exit %d: %s", code, f.err.String())
	}
	if _, err := os.Stat(f.getenv["KEYTURN_STORE"]); err != nil {
		t.Errorf("store should exist at $KEYTURN_STORE: %v", err)
	}
}

func TestCorruptStoreExits3(t *testing.T) {
	f := newFixture(t)
	os.WriteFile(f.store, []byte("not json"), 0o600)
	if code := f.run("list"); code != ExitRuntime {
		t.Fatalf("exit %d, want %d", code, ExitRuntime)
	}
}

func TestCreateDoesNotOverwriteOnIDCollision(t *testing.T) {
	// Two creates with identically-seeded randomness would mint the
	// same ID; the CLI must regenerate, not silently replace record #1.
	f := newFixture(t)
	f.mustCreate(t, "--name", "first")
	f.app.Rand = &seqReader{} // reset: same bytes again
	f.mustCreate(t, "--name", "second")
	f.run("list")
	if !strings.Contains(f.out.String(), "first") || !strings.Contains(f.out.String(), "second") {
		t.Fatalf("both keys must survive:\n%s", f.out.String())
	}
}

// keyID extracts the ID segment from a full key string.
func keyID(t *testing.T, full string) string {
	t.Helper()
	segs := strings.Split(full, "_")
	if len(segs) < 3 {
		t.Fatalf("bad key %q", full)
	}
	return segs[len(segs)-2]
}
