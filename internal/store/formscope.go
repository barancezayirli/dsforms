package store

import (
	"strings"
)

// FormScope bounds a query to a set of forms.
//
// It exists for API tokens: a token given to an MCP client can be limited to
// one form's submissions, so that a client talked into exfiltrating an inbox
// loses one form rather than everything ever collected. That is the only
// control on this endpoint that still works after the client has stopped
// behaving, which is why the filter goes into SQL rather than being applied to
// rows on the way back.
//
// **The zero value matches nothing.** Not every form — nothing. A read path
// added later that forgets to pass a scope returns an empty page and is
// noticed; the alternative failure is silent and hands over every form's data.
// ReadFilter.clause sets the same precedent one file over, for the same reason.
type FormScope struct {
	all bool
	ids []string
}

// AllForms is the unbounded scope. Spelled out rather than left as the zero
// value, so that reaching everything is always something a caller asked for.
func AllForms() FormScope { return FormScope{all: true} }

// OnlyForms bounds a query to these form ids.
//
// OnlyForms(nil) matches nothing, which is the same answer as the zero value
// and deliberately not "all of them": an empty list is a caller that named no
// forms, and reading that as permission would invert the whole type.
func OnlyForms(ids []string) FormScope {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return FormScope{ids: out}
}

// ParseFormScope reads the scope out of an api_tokens.form_ids column.
//
// An empty column means every form. It has to: that is what the column holds
// for every token that existed before scoping did, and an upgrade that silently
// revoked live credentials would be worse than the feature is good. This is the
// single place that reading is made — everywhere else, "no forms" means no
// forms — so the exception is one function rather than a rule every caller has
// to remember.
func ParseFormScope(stored string) FormScope {
	if strings.TrimSpace(stored) == "" {
		return AllForms()
	}
	return OnlyForms(strings.Split(stored, ","))
}

// All reports whether this scope reaches every form.
func (f FormScope) All() bool { return f.all }

// IDs are the forms this scope names, empty when it reaches all of them.
func (f FormScope) IDs() []string { return f.ids }

// Allows reports whether one form is in scope. For the single-row paths, where
// there is no page to filter and the check is simply "is this mine".
func (f FormScope) Allows(formID string) bool {
	if f.all {
		return true
	}
	for _, id := range f.ids {
		if id == formID {
			return true
		}
	}
	return false
}

// clause returns the SQL this scope adds to a WHERE, and its arguments.
//
// col is qualified by the caller ("form_id", "s.form_id"), because these
// queries join and an unqualified column would be ambiguous in half of them.
//
// A scope that names nothing returns a clause that is false rather than no
// clause at all. That is the difference between "filter to nothing" and "do not
// filter", and returning the empty string for both is exactly how a zero value
// would come to mean everything.
func (f FormScope) clause(col string) (string, []any) {
	if f.all {
		return "", nil
	}
	if len(f.ids) == 0 {
		return " AND 1 = 0", nil
	}
	args := make([]any, 0, len(f.ids))
	for _, id := range f.ids {
		args = append(args, id)
	}
	return " AND " + col + " IN (?" + strings.Repeat(",?", len(f.ids)-1) + ")", args
}

// String renders the scope for a log line or a listing.
func (f FormScope) String() string {
	if f.all {
		return "all forms"
	}
	if len(f.ids) == 0 {
		return "no forms"
	}
	return strings.Join(f.ids, ",")
}
