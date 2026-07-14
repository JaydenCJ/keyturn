// Tests for scope validation and wildcard matching. Scopes are the
// authorization surface — false positives here become privilege
// escalation, so the wildcard edge cases get particular attention.
package scope

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateAcceptsWellFormedScopes(t *testing.T) {
	for _, s := range []string{"read", "read:users", "billing:invoices:create", "read:*", "*", "a-b:c_d", "v2:items"} {
		if err := Validate(s); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", s, err)
		}
	}
}

func TestValidateRejectsBadScopes(t *testing.T) {
	cases := []string{
		"",                       // empty
		"Read:users",             // uppercase
		"read users",             // space
		"read:",                  // trailing empty segment
		":users",                 // leading empty segment
		"read:*:users",           // wildcard not final
		"read:us*rs",             // wildcard inside a segment
		"read:ユーザー",              // non-ASCII
		strings.Repeat("a", 129), // over the length cap
	}
	for _, s := range cases {
		if err := Validate(s); err == nil {
			t.Errorf("Validate(%q) should fail", s)
		}
	}
}

func TestValidateAllRejectsDuplicates(t *testing.T) {
	if err := ValidateAll([]string{"read:users", "read:users"}); err == nil {
		t.Error("duplicate scopes should be rejected")
	}
	if err := ValidateAll([]string{"read:users", "write:users"}); err != nil {
		t.Errorf("distinct scopes should pass: %v", err)
	}
}

func TestGrantsExactMatch(t *testing.T) {
	if !Grants("read:users", "read:users") {
		t.Error("identical scopes must grant")
	}
	if Grants("read:users", "read:orders") {
		t.Error("sibling scopes must not grant")
	}
	if Grants("read:users", "read") {
		t.Error("a child scope must not grant its parent")
	}
}

func TestGrantsTrailingWildcard(t *testing.T) {
	if !Grants("read:*", "read:users") {
		t.Error("read:* must grant read:users")
	}
	if !Grants("read:*", "read:users:42") {
		t.Error("read:* must grant deeper descendants")
	}
	if Grants("read:*", "write:users") {
		t.Error("read:* must not grant write:users")
	}
	for _, want := range []string{"read", "read:users", "a:b:c:d"} {
		if !Grants("*", want) {
			t.Errorf("* must grant %q", want)
		}
	}
}

func TestWildcardDoesNotGrantitsBarePrefix(t *testing.T) {
	// "read:*" means "read:<one or more segments>" — granting bare
	// "read" would silently widen the permission.
	if Grants("read:*", "read") {
		t.Error("read:* must not grant bare read")
	}
}

func TestDemandedWildcardOnlyMatchesLiteralGrant(t *testing.T) {
	// A caller demanding "read:*" is asking for the wildcard itself;
	// a key holding only "read:users" must not satisfy it.
	if Grants("read:users", "read:*") {
		t.Error("literal grant must not satisfy a demanded wildcard")
	}
	if !Grants("read:*", "read:*") {
		t.Error("identical wildcard must satisfy itself")
	}
	if !Grants("*", "read:*") {
		t.Error("global wildcard must satisfy any demand")
	}
}

func TestMissingReportsUnsatisfiedScopesInDemandOrder(t *testing.T) {
	granted := []string{"read:*", "write:logs"}
	demanded := []string{"admin:all", "read:users", "delete:logs"}
	got := Missing(granted, demanded)
	want := []string{"admin:all", "delete:logs"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Missing = %v, want %v", got, want)
	}
	if got := Missing(nil, nil); len(got) != 0 {
		t.Errorf("no demand must yield no missing scopes, got %v", got)
	}
	if got := Missing(nil, []string{"read"}); !reflect.DeepEqual(got, []string{"read"}) {
		t.Errorf("scopeless key must miss every demand, got %v", got)
	}
}
