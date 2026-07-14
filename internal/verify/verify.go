// Package verify implements the verification pipeline — the one
// decision keyturn exists to make.
//
// Check runs the presented key through a fixed ladder:
//
//	parse → lookup → hash match → disabled? → expired? → scopes → rate limit
//
// The first failing rung decides the (single, stable) denial reason.
// Unknown IDs and wrong secrets both return "not_found" on purpose:
// distinguishing them would tell an attacker which IDs exist. The
// pipeline is pure — clock injected, bucket state in/out — so every
// branch is unit-testable without a server or a wall clock.
package verify

import (
	"time"

	"github.com/JaydenCJ/keyturn/internal/key"
	"github.com/JaydenCJ/keyturn/internal/rate"
	"github.com/JaydenCJ/keyturn/internal/scope"
	"github.com/JaydenCJ/keyturn/internal/store"
)

// Reason is the stable machine-readable denial code.
type Reason string

// The complete set of denial reasons; "" means the key is valid.
const (
	ReasonNone         Reason = ""
	ReasonMalformed    Reason = "malformed"
	ReasonNotFound     Reason = "not_found"
	ReasonDisabled     Reason = "disabled"
	ReasonExpired      Reason = "expired"
	ReasonMissingScope Reason = "missing_scope"
	ReasonRateLimited  Reason = "rate_limited"
)

// Request is one verification question.
type Request struct {
	// Key is the full presented key string.
	Key string
	// Scopes the caller demands; empty means "any valid key".
	Scopes []string
	// Cost is how many tokens this call spends (min 1, default 1).
	Cost float64
}

// Result is the verification answer. It is safe to return to the
// service that asked; it never contains key material.
type Result struct {
	Valid bool
	// Reason is set exactly when Valid is false.
	Reason Reason
	// KeyID / Name / Label / Scopes / Meta describe the matched key.
	// They are populated for every outcome past the hash-match rung —
	// a rate-limited caller still learns who it is.
	KeyID  string
	Name   string
	Label  string
	Scopes []string
	Meta   map[string]string
	// MissingScopes lists the demanded scopes the key lacks
	// (missing_scope only).
	MissingScopes []string
	// Remaining is whole tokens left after this call; -1 = unlimited.
	Remaining int64
	// RetryAfter hints when to retry (rate_limited only). Negative
	// means never: the cost exceeds the bucket's burst capacity.
	RetryAfter time.Duration
}

// Lookup resolves a key ID to its record. store.Store satisfies it.
type Lookup interface {
	Get(id string) (store.Record, error)
	Update(id string, fn func(store.Record) store.Record) error
}

// Check runs the pipeline at instant `now` against `keys`. On the
// rate-limit rung it commits the new bucket state through Update, so
// concurrent verifications of one key serialize on the store lock.
func Check(keys Lookup, req Request, now time.Time) Result {
	parsed, err := key.Parse(req.Key)
	if err != nil {
		return Result{Reason: ReasonMalformed, Remaining: -1}
	}
	rec, err := keys.Get(parsed.ID)
	if err != nil {
		return Result{Reason: ReasonNotFound, Remaining: -1}
	}
	if !key.Matches(rec.Hash, req.Key) {
		// Same public answer as an unknown ID — see the package comment.
		return Result{Reason: ReasonNotFound, Remaining: -1}
	}

	res := Result{
		KeyID:     rec.ID,
		Name:      rec.Name,
		Label:     rec.Label,
		Scopes:    rec.Scopes,
		Meta:      rec.Meta,
		Remaining: -1,
	}
	if rec.Disabled {
		res.Reason = ReasonDisabled
		return res
	}
	if rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt) {
		res.Reason = ReasonExpired
		return res
	}
	if missing := scope.Missing(rec.Scopes, req.Scopes); len(missing) > 0 {
		res.Reason = ReasonMissingScope
		res.MissingScopes = missing
		return res
	}
	if rec.Limit.IsZero() {
		res.Valid = true
		return res
	}

	// Spend tokens under the store lock so two concurrent requests can
	// never both read the same bucket snapshot.
	var dec rate.Decision
	err = keys.Update(rec.ID, func(r store.Record) store.Record {
		var next rate.State
		dec, next = rate.Take(r.Limit, r.Bucket, req.Cost, now)
		r.Bucket = next
		return r
	})
	if err != nil {
		// The key vanished between Get and Update (concurrent delete).
		return Result{Reason: ReasonNotFound, Remaining: -1}
	}
	res.Remaining = dec.Remaining
	if !dec.Allowed {
		res.Reason = ReasonRateLimited
		res.RetryAfter = dec.RetryAfter
		return res
	}
	res.Valid = true
	return res
}
