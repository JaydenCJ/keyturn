// Tests for the token bucket. Take is pure over an injected clock, so
// every scenario — refill, burst clamping, retry hints — is asserted
// at exact instants with zero sleeps.
package rate

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func limit(n float64, w time.Duration) Limit {
	return Limit{Requests: n, Window: w, Burst: n}
}

func TestSequentialTakesDrainAFreshFullBucket(t *testing.T) {
	// A never-used key starts with its whole burst available…
	d, s := Take(limit(10, time.Minute), State{}, 1, t0)
	if !d.Allowed || d.Remaining != 9 {
		t.Fatalf("fresh bucket: allowed=%v remaining=%d, want allowed 9", d.Allowed, d.Remaining)
	}
	if s.Updated != t0.UnixNano() {
		t.Errorf("state timestamp not advanced")
	}
	// …and drains one token per take until empty.
	l := limit(3, time.Minute)
	s = State{}
	for i := 0; i < 3; i++ {
		d, s = Take(l, s, 1, t0)
		if !d.Allowed {
			t.Fatalf("take %d should be allowed", i+1)
		}
	}
	d, _ = Take(l, s, 1, t0)
	if d.Allowed {
		t.Fatal("fourth take at the same instant must be denied")
	}
	if d.Remaining != 0 {
		t.Errorf("remaining = %d, want 0", d.Remaining)
	}
}

func TestRefillIsContinuous(t *testing.T) {
	l := limit(60, time.Minute) // 1 token per second
	s := State{}
	// Drain completely.
	var d Decision
	d, s = Take(l, s, 60, t0)
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("drain: allowed=%v remaining=%d", d.Allowed, d.Remaining)
	}
	// Half a second later: 0.5 tokens — not enough for one.
	d, _ = Take(l, s, 1, t0.Add(500*time.Millisecond))
	if d.Allowed {
		t.Fatal("0.5 tokens must not satisfy cost 1")
	}
	// A full second later: exactly 1 token.
	d, _ = Take(l, s, 1, t0.Add(time.Second))
	if !d.Allowed {
		t.Fatal("1 token after 1s at 60/1m must be allowed")
	}
	// And an hour later the bucket holds burst (60), not 3600.
	if got := Peek(l, s, t0.Add(time.Hour)); got != 60 {
		t.Errorf("Peek after long idle = %d, want burst 60", got)
	}
}

func TestRetryAfterIsExact(t *testing.T) {
	l := limit(60, time.Minute) // 1 token/second
	_, s := Take(l, State{}, 60, t0)
	d, _ := Take(l, s, 1, t0)
	if d.Allowed {
		t.Fatal("empty bucket must deny")
	}
	if d.RetryAfter != time.Second {
		t.Errorf("RetryAfter = %v, want exactly 1s for a 1-token deficit", d.RetryAfter)
	}
	// And the hint must be honest: taking at t0+RetryAfter succeeds.
	d2, _ := Take(l, s, 1, t0.Add(d.RetryAfter))
	if !d2.Allowed {
		t.Error("retrying at the hinted instant must succeed")
	}
}

func TestCostAboveBurstIsDeniedForever(t *testing.T) {
	l := limit(5, time.Minute)
	d, _ := Take(l, State{}, 6, t0)
	if d.Allowed {
		t.Fatal("cost above burst can never be satisfied")
	}
	if d.RetryAfter != -1 {
		t.Errorf("RetryAfter = %v, want -1 (never)", d.RetryAfter)
	}
}

func TestCostBelowOneCountsAsOne(t *testing.T) {
	l := limit(2, time.Minute)
	d, s := Take(l, State{}, 0, t0) // default cost
	if !d.Allowed || d.Remaining != 1 {
		t.Fatalf("cost 0 should spend 1 token: remaining=%d", d.Remaining)
	}
	d, _ = Take(l, s, -5, t0)
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("negative cost should spend 1 token: remaining=%d", d.Remaining)
	}
}

func TestZeroLimitIsUnlimited(t *testing.T) {
	d, s := Take(Limit{}, State{}, 100, t0)
	if !d.Allowed {
		t.Fatal("unlimited key must always be allowed")
	}
	if d.Remaining != -1 {
		t.Errorf("remaining = %d, want -1 (unlimited)", d.Remaining)
	}
	if s != (State{}) {
		t.Error("unlimited take must not touch bucket state")
	}
}

func TestBurstAllowsSpikesBeyondRate(t *testing.T) {
	// 1/1m sustained but burst 10: ten instant calls pass, the 11th fails.
	l := Limit{Requests: 1, Window: time.Minute, Burst: 10}
	s := State{}
	var d Decision
	for i := 0; i < 10; i++ {
		d, s = Take(l, s, 1, t0)
		if !d.Allowed {
			t.Fatalf("burst call %d should pass", i+1)
		}
	}
	if d, _ = Take(l, s, 1, t0); d.Allowed {
		t.Fatal("call 11 must be rate-limited")
	}
}

func TestClockGoingBackwardsDoesNotMintTokens(t *testing.T) {
	l := limit(10, time.Minute)
	_, s := Take(l, State{}, 10, t0)
	d, _ := Take(l, s, 1, t0.Add(-time.Hour))
	if d.Allowed {
		t.Fatal("a clock jump backwards must not refill the bucket")
	}
}

func TestParseLimitAcceptsCommonForms(t *testing.T) {
	cases := map[string]Limit{
		"100/1m":    {Requests: 100, Window: time.Minute, Burst: 100},
		"10/s":      {Requests: 10, Window: time.Second, Burst: 10},
		"3/500ms":   {Requests: 3, Window: 500 * time.Millisecond, Burst: 3},
		"5000/h":    {Requests: 5000, Window: time.Hour, Burst: 5000},
		"":          {},
		"unlimited": {},
	}
	for spec, want := range cases {
		got, err := ParseLimit(spec)
		if err != nil {
			t.Errorf("ParseLimit(%q): %v", spec, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLimit(%q) = %+v, want %+v", spec, got, want)
		}
	}
	// String() round-trips the same syntax.
	l, _ := ParseLimit("100/1m")
	if got := l.String(); got != "100/1m" {
		t.Errorf("String() = %q, want 100/1m", got)
	}
	l.Burst = 250
	if got := l.String(); got != "100/1m (burst 250)" {
		t.Errorf("String() = %q, want burst suffix", got)
	}
	if got := (Limit{}).String(); got != "unlimited" {
		t.Errorf("zero limit String() = %q, want unlimited", got)
	}
}

func TestParseLimitRejectsGarbage(t *testing.T) {
	for _, spec := range []string{"/1m", "100/", "100", "0/1m", "-5/1m", "abc/1m", "100/xyz", "100/0s", "100/-1m"} {
		if _, err := ParseLimit(spec); err == nil {
			t.Errorf("ParseLimit(%q) should fail", spec)
		}
	}
}
