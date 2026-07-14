// Package scope implements keyturn's permission scopes.
//
// A scope is a lowercase string of ':'-separated segments, e.g.
// "read:users" or "billing:invoices:create". Keys are granted scopes;
// verification requests demand scopes. A granted scope satisfies a
// demanded scope when the segments match exactly, or when the granted
// scope's final segment is "*" and every earlier segment matches
// ("read:*" covers "read:users" and "read:users:42"; "*" covers all).
// Wildcards are only meaningful on the granted side — a demanded
// wildcard matches nothing but a literal "*" grant.
package scope

import (
	"fmt"
	"strings"
)

// maxLen bounds a single scope string; anything longer is rejected at
// validation so a hostile client can't stuff megabytes into a request.
const maxLen = 128

// Validate checks that s is a well-formed scope: non-empty ':'-separated
// segments of [a-z0-9_-], with '*' allowed only as the final segment.
func Validate(s string) error {
	if s == "" {
		return fmt.Errorf("scope is empty")
	}
	if len(s) > maxLen {
		return fmt.Errorf("scope longer than %d chars", maxLen)
	}
	segs := strings.Split(s, ":")
	for i, seg := range segs {
		if seg == "" {
			return fmt.Errorf("scope %q has an empty segment", s)
		}
		if seg == "*" {
			if i != len(segs)-1 {
				return fmt.Errorf("scope %q: '*' is only valid as the final segment", s)
			}
			continue
		}
		for _, r := range seg {
			ok := r == '_' || r == '-' ||
				(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !ok {
				return fmt.Errorf("scope %q: invalid character %q", s, r)
			}
		}
	}
	return nil
}

// ValidateAll validates every scope in the slice and rejects duplicates.
func ValidateAll(scopes []string) error {
	seen := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		if err := Validate(s); err != nil {
			return err
		}
		if seen[s] {
			return fmt.Errorf("duplicate scope %q", s)
		}
		seen[s] = true
	}
	return nil
}

// Grants reports whether the granted scope satisfies the demanded scope.
func Grants(granted, demanded string) bool {
	if granted == demanded {
		return true
	}
	g := strings.Split(granted, ":")
	d := strings.Split(demanded, ":")
	if g[len(g)-1] != "*" {
		return false
	}
	prefix := g[:len(g)-1]
	// "read:*" needs at least "read:<something>": the wildcard stands in
	// for one or more segments, never zero — "read:*" does not grant "read".
	if len(d) <= len(prefix) {
		return false
	}
	for i, seg := range prefix {
		if d[i] != seg {
			return false
		}
	}
	return true
}

// Missing returns the demanded scopes that no granted scope satisfies,
// in demand order. An empty result means the grant covers the demand.
func Missing(granted, demanded []string) []string {
	var missing []string
	for _, want := range demanded {
		ok := false
		for _, have := range granted {
			if Grants(have, want) {
				ok = true
				break
			}
		}
		if !ok {
			missing = append(missing, want)
		}
	}
	return missing
}
