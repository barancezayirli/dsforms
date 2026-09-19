package mcpserver

import (
	"go/ast"
	"slices"
	"strings"
	"testing"

	"github.com/barancezayirli/dsforms/internal/astcheck"
)

// TestParseScopesDropsWhatItDoesNotUnderstand is the security property of this
// type, stated as a test because a comment saying "fail closed" is not a
// guarantee.
//
// The input is a database column. A value this build cannot interpret — written
// by a newer binary, corrupted, or planted — must reduce what the token can do.
// Anything that survives into the result is a power granted.
func TestParseScopesDropsWhatItDoesNotUnderstand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []string
		want Scopes
	}{
		{"nothing", nil, nil},
		{"empty strings", []string{"", "  "}, nil},
		{"one", []string{"read"}, Scopes{ScopeRead}},
		{"all three", []string{"read", "write", "delete"}, Scopes{ScopeRead, ScopeWrite, ScopeDelete}},
		{"unknown is dropped", []string{"read", "superuser"}, Scopes{ScopeRead}},
		{"only unknowns leaves nothing", []string{"superuser", "*", "admin"}, nil},
		{"case matters", []string{"READ"}, nil},
		{"whitespace is trimmed", []string{" read "}, Scopes{ScopeRead}},
		{"duplicates collapse", []string{"read", "read"}, Scopes{ScopeRead}},
		{"order is canonical", []string{"delete", "read"}, Scopes{ScopeRead, ScopeDelete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseScopes(tt.in)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParseScopes(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestScopeValidDeniesByDefault. The one-line version of the rule AGENT.md §4
// states: in a switch over a closed value set, the safe outcome is never the
// default branch.
func TestScopeValidDeniesByDefault(t *testing.T) {
	t.Parallel()
	for _, s := range []Scope{"", "superuser", "Read", "read ", "*", "write,delete"} {
		if s.Valid() {
			t.Errorf("Scope(%q).Valid() = true; an unrecognised scope must grant nothing", s)
		}
	}
	for _, s := range AllScopes {
		if !s.Valid() {
			t.Errorf("Scope(%q).Valid() = false for a declared constant", s)
		}
	}
}

// TestValidateScopesReportsTypos is the other direction, and the reason there
// are two functions rather than one.
//
// Creating a token is a person stating an intent. Silently granting them less
// than they typed is how someone spends an hour debugging a refusal that the
// form could have named at the point of the typo.
func TestValidateScopesReportsTypos(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      []string
		want    Scopes
		wantErr string
	}{
		{"valid", []string{"read", "write"}, Scopes{ScopeRead, ScopeWrite}, ""},
		{"canonical order", []string{"delete", "read"}, Scopes{ScopeRead, ScopeDelete}, ""},
		{"typo is named", []string{"read", "wirte"}, nil, `"wirte"`},
		{"nothing at all", nil, nil, "at least one scope"},
		{"only blanks", []string{"", " "}, nil, "at least one scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateScopes(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateScopes(%q) error = %v, want nil", tt.in, err)
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("ValidateScopes(%q) = %v, want %v", tt.in, got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateScopes(%q) error = nil, want one mentioning %s", tt.in, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateScopes(%q) error = %q, want it to mention %s", tt.in, err, tt.wantErr)
			}
			if got != nil {
				t.Errorf("ValidateScopes(%q) returned %v alongside an error; a partial grant is worse than none", tt.in, got)
			}
		})
	}
}

// TestHasIsFalseForAnEmptySet. An empty scopes column means a token that can do
// nothing — the value a corrupt or truncated row degrades to, so it has to be
// the safe one.
func TestHasIsFalseForAnEmptySet(t *testing.T) {
	t.Parallel()
	var none Scopes
	for _, s := range AllScopes {
		if none.Has(s) {
			t.Errorf("empty Scopes reports Has(%q) = true", s)
		}
	}
	if ParseScopes(nil).Has(ScopeRead) {
		t.Error("ParseScopes(nil) grants read")
	}
}

// TestEveryScopeIsDescribed. The description is what an operator reads while
// deciding how much power to hand out, and Go will not tell us one is missing:
// a switch with no case for a new constant falls to the default and returns "",
// which renders as a checkbox with no explanation beside it.
func TestEveryScopeIsDescribed(t *testing.T) {
	t.Parallel()
	for _, s := range AllScopes {
		if strings.TrimSpace(s.Describe()) == "" {
			t.Errorf("scope %q has no description", s)
		}
	}
	if Scope("superuser").Describe() != "" {
		t.Error("an unknown scope produced a description")
	}
}

// TestAllScopesIsComplete derives the constant list from this package's own
// source, so the slice cannot fall behind the constants.
//
// Without it AllScopes is a hand-maintained copy of the const block, and
// everything that ranges it — the admin form, the description coverage test, the
// prebuilt server table — silently stops covering a scope the moment someone
// adds one. That is the exact defect internal/screen's AllChecks test exists
// for, and this is the same test against the same shape.
func TestAllScopesIsComplete(t *testing.T) {
	t.Parallel()

	_, files := astcheck.Package(t, ".")
	declared := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				// Only constants whose declared type is Scope.
				ident, ok := vs.Type.(*ast.Ident)
				if !ok || ident.Name != "Scope" {
					continue
				}
				for _, name := range vs.Names {
					declared[name.Name] = true
				}
			}
		}
	}

	if len(declared) == 0 {
		t.Fatal("the scan found no `X Scope = \"...\"` constants; it has stopped matching and this test now requires nothing")
	}
	if len(declared) != len(AllScopes) {
		t.Errorf("the source declares %d Scope constants (%v) but AllScopes lists %d (%v)",
			len(declared), keys(declared), len(AllScopes), AllScopes)
	}
}

// TestAllScopesIsCompleteDetectorFires is the positive control for the scan
// above. A structural test passes in two different worlds — the property holds,
// or the matcher has quietly died — and only a fixture that must trip it tells
// them apart.
func TestAllScopesIsCompleteDetectorFires(t *testing.T) {
	t.Parallel()

	hasScopeConst := func(f *ast.File) bool {
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			if ident, ok := vs.Type.(*ast.Ident); ok && ident.Name == "Scope" {
				found = true
			}
			return true
		})
		return found
	}

	astcheck.Detector{
		Name:  "a constant declared with type Scope",
		Match: hasScopeConst,
		Positive: map[string]string{
			"a scope constant":     "package p\nconst ScopeRead Scope = \"read\"\n",
			"inside a const block": "package p\nconst (\n\tScopeRead Scope = \"read\"\n)\n",
		},
		Negative: map[string]string{
			"an untyped constant": "package p\nconst ScopeRead = \"read\"\n",
			"a different type":    "package p\nconst CheckMarkup Check = \"markup\"\n",
			"a variable":          "package p\nvar AllScopes = []Scope{ScopeRead}\n",
		},
	}.Verify(t)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
