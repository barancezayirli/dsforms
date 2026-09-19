package store

import (
	"strings"
	"testing"
)

// The zero value denying is the property every scoped query rests on. If a read
// path added later forgets to set a scope, it must return nothing and be
// noticed, not quietly hand back every form's submissions.
func TestTheZeroFormScopeMatchesNothing(t *testing.T) {
	t.Parallel()

	var unset FormScope
	tests := []struct {
		name  string
		scope FormScope
		want  bool // does it allow a form?
	}{
		{"zero value", unset, false},
		{"OnlyForms(nil)", OnlyForms(nil), false},
		{"OnlyForms(empty)", OnlyForms([]string{}), false},
		{"OnlyForms of another form", OnlyForms([]string{"other"}), false},
		{"OnlyForms of this form", OnlyForms([]string{"f1"}), true},
		{"AllForms", AllForms(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.scope.Allows("f1"); got != tc.want {
				t.Errorf("Allows(f1) = %v, want %v", got, tc.want)
			}
		})
	}
}

// clause is the half Allows cannot check: a scope that says no to Allows but
// emits no SQL would filter nothing at all.
func TestTheZeroFormScopeEmitsARefusingClause(t *testing.T) {
	t.Parallel()

	var unset FormScope
	for _, tc := range []struct {
		name  string
		scope FormScope
	}{
		{"zero value", unset},
		{"OnlyForms(nil)", OnlyForms(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sql, args := tc.scope.clause("form_id")
			if strings.TrimSpace(sql) == "" {
				t.Fatalf("clause = %q with args %v — an empty clause filters nothing", sql, args)
			}
			if len(args) != 0 {
				t.Errorf("args = %v, want none for a scope that matches nothing", args)
			}
		})
	}

	if sql, args := AllForms().clause("form_id"); sql != "" || len(args) != 0 {
		t.Errorf("AllForms clause = %q %v, want no clause at all", sql, args)
	}

	sql, args := OnlyForms([]string{"a", "b"}).clause("s.form_id")
	if !strings.Contains(sql, "s.form_id") {
		t.Errorf("clause = %q, want it to name the column it was given", sql)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want one per form id", args)
	}
}

// ParseFormScope is the one place "" is read as every form. It has to be,
// because that is what the column holds for every token that existed before
// scoping did, and an upgrade must not silently revoke access.
func TestParseFormScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stored    string
		allowsAny bool
		allowsF1  bool
	}{
		{"empty is every form, for the upgrade path", "", true, true},
		{"whitespace only is also every form", "   ", true, true},
		{"one id", "f1", false, true},
		{"several ids", "f2,f1", false, true},
		{"padded ids", " f2 , f1 ", false, true},
		{"another form's id", "f2", false, false},
		{"separators with nothing between them grant nothing", ",,", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := ParseFormScope(tc.stored)
			if got := scope.Allows("anything-at-all"); got != tc.allowsAny {
				t.Errorf("Allows(arbitrary) = %v, want %v", got, tc.allowsAny)
			}
			if got := scope.Allows("f1"); got != tc.allowsF1 {
				t.Errorf("Allows(f1) = %v, want %v", got, tc.allowsF1)
			}
		})
	}
}

// A scoped token is the reason this type exists, so the round trip through
// storage has to preserve exactly which forms it named.
func TestAPITokensCarryTheirForms(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	_, scoped, err := s.CreateAPIToken(u.ID, "one form", []string{"read"}, []string{"f2", "f1"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if len(scoped.FormIDs) != 2 {
		t.Fatalf("FormIDs = %v, want both", scoped.FormIDs)
	}

	raw, _, err := s.CreateAPIToken(u.ID, "every form", []string{"read"}, nil, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	// nil is "every form" on the way in, because that is what the admin form
	// sends when the operator leaves it at All forms.
	back, err := s.GetAPIToken(raw)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if len(back.FormIDs) != 0 {
		t.Errorf("FormIDs = %v, want none recorded", back.FormIDs)
	}
	if !back.Scope().Allows("anything") {
		t.Error("a token created with no forms named cannot reach any form")
	}

	listed, err := s.ListAPITokens(u.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	var found bool
	for _, tok := range listed {
		if tok.ID == scoped.ID {
			found = true
			if tok.Scope().Allows("f3") || !tok.Scope().Allows("f1") {
				t.Errorf("listed scope is wrong: %v", tok.FormIDs)
			}
		}
	}
	if !found {
		t.Fatalf("the scoped token is not in the listing")
	}
}
