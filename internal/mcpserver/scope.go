package mcpserver

import (
	"fmt"
	"slices"
	"strings"
)

// Scope is one class of power a token may carry.
//
// A defined type with an AllScopes slice beside it, for the reason
// internal/screen states for Check: Go exhaustiveness-checks neither a map
// literal keyed by a named type nor a switch over one, so the type alone buys
// nothing. What makes the set checkable is the slice, ranged by the tests, plus
// Valid below naming every case so a new constant with no decision cannot
// silently take whichever branch was written last.
type Scope string

const (
	// ScopeRead can list and read submissions, forms, filter rules and
	// aggregate statistics. It changes nothing.
	ScopeRead Scope = "read"

	// ScopeWrite can change a submission's read state, mark one as spam, and add
	// a block rule. Everything it does is reversible from the admin.
	ScopeWrite Scope = "write"

	// ScopeDelete can destroy submissions permanently. Separate from ScopeWrite
	// precisely because it is the one class of action nothing can undo — a token
	// that files spam should not also be able to erase the evidence.
	ScopeDelete Scope = "delete"
)

// AllScopes is every declared Scope, in the order the admin UI offers them —
// least to most dangerous. Anything walking the set ranges this rather than
// restating it.
var AllScopes = []Scope{ScopeRead, ScopeWrite, ScopeDelete}

// Valid reports whether s is a scope this build understands.
//
// Every case is named and the default denies. A scope column written by a newer
// binary, or corrupted, must grant nothing — the alternative is a value we
// cannot reason about taking the permissive branch.
func (s Scope) Valid() bool {
	switch s {
	case ScopeRead, ScopeWrite, ScopeDelete:
		return true
	default:
		return false
	}
}

// Describe is the one-line explanation shown beside the checkbox when a token is
// created. It lives here rather than in the template so the wording cannot fall
// behind the constant it describes.
func (s Scope) Describe() string {
	switch s {
	case ScopeRead:
		return "Read submissions, forms, filter rules and statistics."
	case ScopeWrite:
		return "Mark messages read or unread, mark one as spam, and add block rules."
	case ScopeDelete:
		return "Delete submissions permanently. This cannot be undone."
	default:
		return ""
	}
}

// Caution is what this scope costs when the client holding it is not the one
// the operator meant — a compromised client, a careless one, or one acting on
// text a stranger wrote into a form.
//
// Separate from Describe because the two answer different questions and an
// operator ticking boxes needs both. Describe says what the scope lets a client
// do, which reads as a feature list; Caution says what it loses, which is the
// half that changes a decision.
//
// Read's wording is the one that matters. It is the scope handed out most
// freely and the only one that can lose everything ever collected, permanently,
// because a model that has been told to forward an inbox needs no write access
// to do it — it needs the inbox. Presenting read as the harmless option is the
// mistake this text exists to prevent, so it never calls it safe.
func (s Scope) Caution() string {
	switch s {
	case ScopeRead:
		return "This is every submission you have ever collected. A client that is talked into forwarding them needs nothing else."
	case ScopeWrite:
		return "A client acting on a submission's instructions could file real messages as spam or add block rules. All of it is reversible from the admin."
	case ScopeDelete:
		return "Nothing undoes this. Withhold it unless a client genuinely needs it — deleting from the admin costs you nothing."
	default:
		return ""
	}
}

// Scopes is a set of scopes, already filtered to the ones this build understands.
type Scopes []Scope

// ParseScopes turns stored strings into a Scopes, dropping anything unrecognised.
//
// Dropping rather than erroring is deliberate and is the whole security posture
// of this type in one line: the input comes from a database column, so the
// failure mode to design against is a value this build cannot interpret. Such a
// value must reduce what a token can do, never expand it. An error return would
// push that decision to callers, and the caller that gets it wrong grants
// everything.
//
// The result is deduplicated and in AllScopes order, so two tokens with the same
// powers have the same Scopes whatever order they were stored in.
func ParseScopes(raw []string) Scopes {
	seen := map[Scope]bool{}
	for _, r := range raw {
		s := Scope(strings.TrimSpace(r))
		if s.Valid() {
			seen[s] = true
		}
	}
	var out Scopes
	for _, s := range AllScopes {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out
}

// Has reports whether the set carries a scope. A nil or empty Scopes has none,
// which is what an empty scopes column means: a token that can do nothing.
func (s Scopes) Has(want Scope) bool {
	return slices.Contains(s, want)
}

// Strings renders the set for storage and for display, in AllScopes order.
func (s Scopes) Strings() []string {
	out := make([]string, 0, len(s))
	for _, sc := range s {
		out = append(out, string(sc))
	}
	return out
}

// String renders the set for a log line.
func (s Scopes) String() string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s.Strings(), ",")
}

// Key is the canonical identity of a scope set, used to select the prebuilt
// server that advertises exactly these tools.
func (s Scopes) Key() string {
	return strings.Join(s.Strings(), ",")
}

// ValidateScopes is ParseScopes for the two paths that *create* a token — the
// admin form and the CLI — where an unrecognised scope is a typo to report
// rather than a value to silently drop.
//
// The two directions are deliberately different. Creating a token is a person
// stating an intent, and silently granting them less than they asked for is how
// someone ends up debugging a 403 for an hour. Reading a token back is a
// machine interpreting storage, where dropping is the only safe move. Same
// value set, opposite handling of the unknown, because the question is not the
// same question.
//
// At least one scope is required: a token with none can call nothing, which is
// never what anyone means to create.
func ValidateScopes(raw []string) (Scopes, error) {
	var unknown []string
	seen := map[Scope]bool{}
	for _, r := range raw {
		trimmed := strings.TrimSpace(r)
		if trimmed == "" {
			continue
		}
		s := Scope(trimmed)
		if !s.Valid() {
			unknown = append(unknown, trimmed)
			continue
		}
		seen[s] = true
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown scope %s (valid scopes are %s)",
			strings.Join(quoteAll(unknown), ", "), Scopes(AllScopes).String())
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("a token needs at least one scope (valid scopes are %s)",
			Scopes(AllScopes).String())
	}

	var out Scopes
	for _, s := range AllScopes {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

func quoteAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}
