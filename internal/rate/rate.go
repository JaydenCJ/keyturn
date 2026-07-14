// Package rate implements the per-key token bucket.
//
// Everything here is pure: Take is a function of (limit, state, cost,
// now) → (decision, new state). No goroutines, no wall clock — callers
// inject time, which is what makes the whole limiter deterministic
// under test and trivially embeddable in the store or the server.
package rate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Limit is a per-key rate limit: Requests tokens refill continuously
// over Window, and the bucket holds at most Burst tokens.
type Limit struct {
	Requests float64       `json:"requests"`
	Window   time.Duration `json:"window_ns"`
	Burst    float64       `json:"burst"`
}

// IsZero reports whether no limit is configured (unlimited key).
func (l Limit) IsZero() bool { return l.Requests == 0 }

// String renders the limit in the same "N/window" syntax Parse accepts,
// with "(burst M)" appended when burst differs from N.
func (l Limit) String() string {
	if l.IsZero() {
		return "unlimited"
	}
	s := fmt.Sprintf("%s/%s", trimFloat(l.Requests), shortWindow(l.Window))
	if l.Burst != l.Requests {
		s += fmt.Sprintf(" (burst %s)", trimFloat(l.Burst))
	}
	return s
}

// State is the persisted bucket state for one key.
type State struct {
	// Tokens remaining at the instant of Updated.
	Tokens float64 `json:"tokens"`
	// Updated is the Unix-nanosecond timestamp of the last Take.
	Updated int64 `json:"updated"`
}

// Decision is the outcome of one Take.
type Decision struct {
	Allowed bool
	// Remaining is the whole number of tokens left after this Take.
	Remaining int64
	// RetryAfter is how long until the bucket holds `cost` tokens again;
	// zero when Allowed.
	RetryAfter time.Duration
}

// Take attempts to spend cost tokens at instant now. A zero Limit always
// allows and reports -1 remaining (unlimited). Cost below 1 counts as 1.
func Take(l Limit, s State, cost float64, now time.Time) (Decision, State) {
	if l.IsZero() {
		return Decision{Allowed: true, Remaining: -1}, s
	}
	if cost < 1 {
		cost = 1
	}
	tokens := refill(l, s, now)
	if cost > l.Burst {
		// The bucket can never hold this many tokens: deny forever
		// rather than reporting a bogus finite retry hint.
		s = State{Tokens: tokens, Updated: now.UnixNano()}
		return Decision{Allowed: false, Remaining: floor64(tokens), RetryAfter: -1}, s
	}
	if tokens >= cost {
		tokens -= cost
		s = State{Tokens: tokens, Updated: now.UnixNano()}
		return Decision{Allowed: true, Remaining: floor64(tokens)}, s
	}
	deficit := cost - tokens
	perToken := float64(l.Window) / l.Requests
	retry := time.Duration(math.Ceil(deficit * perToken))
	s = State{Tokens: tokens, Updated: now.UnixNano()}
	return Decision{Allowed: false, Remaining: floor64(tokens), RetryAfter: retry}, s
}

// Peek returns the whole tokens available at instant now without
// spending any. Unlimited keys report -1.
func Peek(l Limit, s State, now time.Time) int64 {
	if l.IsZero() {
		return -1
	}
	return floor64(refill(l, s, now))
}

// refill advances the bucket to `now`. A zero-valued State (never used)
// starts full — a fresh key gets its whole burst immediately.
func refill(l Limit, s State, now time.Time) float64 {
	if s.Updated == 0 {
		return l.Burst
	}
	elapsed := now.UnixNano() - s.Updated
	if elapsed <= 0 {
		return clamp(s.Tokens, l.Burst)
	}
	rate := l.Requests / float64(l.Window)
	return clamp(s.Tokens+float64(elapsed)*rate, l.Burst)
}

// ParseLimit parses "N/window" — e.g. "100/1m", "10/s", "5000/24h",
// "3/500ms". Burst defaults to N and can be set separately by callers.
func ParseLimit(spec string) (Limit, error) {
	if spec == "" || spec == "unlimited" {
		return Limit{}, nil
	}
	slash := strings.IndexByte(spec, '/')
	if slash <= 0 || slash == len(spec)-1 {
		return Limit{}, fmt.Errorf("rate %q: want N/window, e.g. 100/1m", spec)
	}
	n, err := strconv.ParseFloat(spec[:slash], 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) {
		return Limit{}, fmt.Errorf("rate %q: request count must be a positive number", spec)
	}
	win := spec[slash+1:]
	// Allow a bare unit ("s", "m", "h") as shorthand for one of it.
	if win != "" && (win[0] < '0' || win[0] > '9') {
		win = "1" + win
	}
	d, err := time.ParseDuration(win)
	if err != nil || d <= 0 {
		return Limit{}, fmt.Errorf("rate %q: bad window (use ms, s, m, h)", spec)
	}
	return Limit{Requests: n, Window: d, Burst: n}, nil
}

func clamp(v, max float64) float64 {
	if v > max {
		return max
	}
	return v
}

func floor64(v float64) int64 { return int64(math.Floor(v)) }

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func shortWindow(d time.Duration) string {
	switch {
	case d == time.Hour:
		return "1h"
	case d == time.Minute:
		return "1m"
	case d == time.Second:
		return "1s"
	default:
		return d.String()
	}
}
