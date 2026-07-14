// Package server exposes keyturn over HTTP: one public verification
// endpoint plus a bearer-token-guarded admin surface for key CRUD.
//
// The verification endpoint answers every definitive question with 200
// and a JSON body carrying valid/reason — a denied key is a successful
// verification, not a transport error — so proxies and middleware only
// treat non-200 as "the sidecar itself is broken".
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/JaydenCJ/keyturn/internal/issue"
	"github.com/JaydenCJ/keyturn/internal/rate"
	"github.com/JaydenCJ/keyturn/internal/store"
	"github.com/JaydenCJ/keyturn/internal/verify"
	"github.com/JaydenCJ/keyturn/internal/version"
)

// maxBodyBytes caps request bodies; verification payloads are tiny.
const maxBodyBytes = 64 << 10

// Server wires the store to HTTP handlers.
type Server struct {
	Store *store.Store
	// AdminToken guards /v1/keys*. Empty disables the whole admin
	// surface (verify-only mode) rather than leaving it open.
	AdminToken string
	// Now is the clock; tests pin it. Defaults to time.Now.
	Now func() time.Time
	// Rand seeds key generation; nil means crypto/rand.
	Rand io.Reader
	// Logger receives one line per admin mutation. Nil silences it.
	Logger *log.Logger
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/verify", s.handleVerify)
	mux.HandleFunc("POST /v1/keys", s.admin(s.handleCreate))
	mux.HandleFunc("GET /v1/keys", s.admin(s.handleList))
	mux.HandleFunc("GET /v1/keys/{id}", s.admin(s.handleGet))
	mux.HandleFunc("POST /v1/keys/{id}/revoke", s.admin(s.handleSetDisabled(true)))
	mux.HandleFunc("POST /v1/keys/{id}/enable", s.admin(s.handleSetDisabled(false)))
	mux.HandleFunc("DELETE /v1/keys/{id}", s.admin(s.handleDelete))
	return mux
}

// ---- public surface ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": version.Version,
		"keys":    s.Store.Len(),
	})
}

// verifyRequest is the wire shape of a verification question.
type verifyRequest struct {
	Key    string   `json:"key"`
	Scopes []string `json:"scopes,omitempty"`
	Cost   float64  `json:"cost,omitempty"`
}

// VerifyResponse is the wire shape of the answer.
type VerifyResponse struct {
	Valid         bool              `json:"valid"`
	Reason        string            `json:"reason,omitempty"`
	KeyID         string            `json:"key_id,omitempty"`
	Name          string            `json:"name,omitempty"`
	Label         string            `json:"label,omitempty"`
	Scopes        []string          `json:"scopes,omitempty"`
	MissingScopes []string          `json:"missing_scopes,omitempty"`
	Meta          map[string]string `json:"meta,omitempty"`
	// Remaining whole tokens after this call; -1 means unlimited.
	Remaining int64 `json:"remaining"`
	// RetryAfterMS is set when rate_limited; -1 means "never" (the
	// requested cost exceeds the bucket's burst capacity).
	RetryAfterMS *int64 `json:"retry_after_ms,omitempty"`
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "body must include a key")
		return
	}
	res := verify.Check(s.Store, verify.Request{
		Key:    req.Key,
		Scopes: req.Scopes,
		Cost:   req.Cost,
	}, s.now())
	writeJSON(w, http.StatusOK, ToResponse(res))
}

func ToResponse(res verify.Result) VerifyResponse {
	out := VerifyResponse{
		Valid:         res.Valid,
		Reason:        string(res.Reason),
		KeyID:         res.KeyID,
		Name:          res.Name,
		Label:         res.Label,
		Scopes:        res.Scopes,
		MissingScopes: res.MissingScopes,
		Meta:          res.Meta,
		Remaining:     res.Remaining,
	}
	if res.Reason == verify.ReasonRateLimited {
		ms := int64(-1)
		if res.RetryAfter >= 0 {
			// Round up: retrying at the hinted instant must succeed.
			ms = int64((res.RetryAfter + time.Millisecond - 1) / time.Millisecond)
		}
		out.RetryAfterMS = &ms
	}
	return out
}

// ---- admin surface ----

// admin wraps a handler with bearer-token auth. With no token
// configured the whole surface answers 403, and says why.
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" {
			writeError(w, http.StatusForbidden,
				"admin API disabled: start keyturn serve with --admin-token")
			return
		}
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix ||
			subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(s.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or wrong admin token")
			return
		}
		next(w, r)
	}
}

// createRequest is the admin wire shape for minting a key.
type createRequest struct {
	Name    string            `json:"name"`
	Label   string            `json:"label,omitempty"`
	Scopes  []string          `json:"scopes,omitempty"`
	Rate    string            `json:"rate,omitempty"`
	Burst   float64           `json:"burst,omitempty"`
	Expires string            `json:"expires,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// RecordView is the public projection of a record: no hash, no bucket
// internals, limit rendered in the same syntax create accepts.
type RecordView struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Label     string            `json:"label,omitempty"`
	Scopes    []string          `json:"scopes,omitempty"`
	Limit     string            `json:"limit"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	Disabled  bool              `json:"disabled"`
	Meta      map[string]string `json:"meta,omitempty"`
	Remaining int64             `json:"remaining"`
}

// View projects a record at instant now.
func View(r store.Record, now time.Time) RecordView {
	return RecordView{
		ID:        r.ID,
		Name:      r.Name,
		Label:     r.Label,
		Scopes:    r.Scopes,
		Limit:     r.Limit.String(),
		CreatedAt: r.CreatedAt,
		ExpiresAt: r.ExpiresAt,
		Disabled:  r.Disabled,
		Meta:      r.Meta,
		Remaining: rate.Peek(r.Limit, r.Bucket, now),
	}
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !readJSON(w, r, &req) {
		return
	}
	issued, err := issue.Issue(issue.Params{
		Name:    req.Name,
		Label:   req.Label,
		Scopes:  req.Scopes,
		Rate:    req.Rate,
		Burst:   req.Burst,
		Expires: req.Expires,
		Meta:    req.Meta,
	}, s.now(), s.Rand)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Store.Put(issued.Record)
	if err := s.Store.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logf("created key %s (%s)", issued.Record.ID, issued.Record.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		// The only moment the full key ever leaves keyturn.
		"key":    issued.Key.Full,
		"record": View(issued.Record, s.now()),
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	recs := s.Store.List()
	views := make([]RecordView, len(recs))
	for i, rec := range recs {
		views[i] = View(rec, s.now())
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": views})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, View(rec, s.now()))
}

func (s *Server) handleSetDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.Store.SetDisabled(id, disabled); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err := s.Store.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		verb := "enabled"
		if disabled {
			verb = "revoked"
		}
		s.logf("%s key %s", verb, id)
		rec, _ := s.Store.Get(id)
		writeJSON(w, http.StatusOK, View(rec, s.now()))
	}
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.Delete(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.Store.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logf("deleted key %s", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---- JSON plumbing ----

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("body exceeds %d bytes", maxBodyBytes))
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
