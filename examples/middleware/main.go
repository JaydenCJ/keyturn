// Command middleware shows the keyturn integration pattern: a small
// upstream service that authorizes every request with one POST to the
// verification sidecar. Run `keyturn serve` first (see ../README.md).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const verifyURL = "http://127.0.0.1:7710/v1/verify"

// verifyResponse mirrors the sidecar's answer (the fields we act on).
type verifyResponse struct {
	Valid        bool   `json:"valid"`
	Reason       string `json:"reason"`
	Name         string `json:"name"`
	RetryAfterMS *int64 `json:"retry_after_ms"`
}

// requireKey wraps a handler with sidecar verification.
func requireKey(scopes []string, next http.HandlerFunc) http.HandlerFunc {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body, _ := json.Marshal(map[string]any{"key": key, "scopes": scopes})
		resp, err := client.Post(verifyURL, "application/json", bytes.NewReader(body))
		if err != nil {
			// Fail closed: if the sidecar is unreachable, deny.
			http.Error(w, "authorization backend unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		var v verifyResponse
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil {
			http.Error(w, "authorization backend error", http.StatusServiceUnavailable)
			return
		}
		if !v.Valid {
			if v.Reason == "rate_limited" {
				if v.RetryAfterMS != nil && *v.RetryAfterMS >= 0 {
					secs := (*v.RetryAfterMS + 999) / 1000
					w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
				}
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			http.Error(w, "forbidden: "+v.Reason, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func main() {
	http.HandleFunc("GET /reports", requireKey([]string{"read:reports"},
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, `{"reports": ["q1", "q2"]}`)
		}))
	log.Println("protected service on http://127.0.0.1:8080 (GET /reports)")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", nil))
}
