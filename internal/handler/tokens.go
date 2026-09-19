package handler

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/flash"
	"github.com/barancezayirli/dsforms/internal/mcpserver"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

// TokensStore is what the API token screen needs from storage.
//
// Every method here is scoped to one user, and there is deliberately no
// unscoped variant to reach for: a token is a personal credential, and its name
// and scopes describe what someone else's automation is allowed to do.
type TokensStore interface {
	CreateAPIToken(userID, name string, scopes, formIDs []string, expiry time.Duration) (string, store.APIToken, error)
	DeleteAPIToken(userID, id string) (bool, error)
	ListAPITokens(userID string) ([]store.APIToken, error)

	// ListForms is the exception to the scoping above, and it reads no personal
	// data: the picker has to offer the forms that exist, and the list has to
	// name the ones a token reaches rather than printing ids at the operator.
	ListForms(forms store.FormScope) ([]store.FormSummary, error)
}

// TokensHandler serves the API token screen, where an operator mints and revokes
// the credentials MCP clients use.
type TokensHandler struct {
	Base
	Store TokensStore

	// TTLDays is how long a newly created token lasts, from
	// config.MCPTokenTTLDays. 0 means it never expires.
	TTLDays int

	// MCPEnabled reflects config. The page is reachable either way — an operator
	// should be able to prepare a token before switching the endpoint on, and to
	// revoke one after switching it off — but it says which it is, because a
	// token that silently does nothing is a confusing afternoon.
	MCPEnabled bool
}

// tokenRow is one token as the page renders it.
type tokenRow struct {
	store.APIToken

	// ScopeList and LastUsed are precomputed rather than left to the template.
	// html/template cannot join a slice or branch on a zero time.Time without a
	// helper, and the alternative is the display rule living in two templates.
	ScopeList string
	LastUsed  string
	Expires   string

	// Reach is what this token can see: "All forms", or the names of the ones it
	// is bounded to. Names rather than ids, because an id tells an operator
	// deciding whether to revoke something nothing at all.
	Reach string
}

// scopeOption is one checkbox on the create form.
type scopeOption struct {
	Value       string
	Description string

	// Caution is what the scope costs if the client holding it is not the one
	// you meant. Rendered beneath Description, in the warning style, because
	// the decision an operator is making at this checkbox is a risk one.
	Caution string
}

// tokensData is the list page. It deliberately carries no form state: the
// create form moved to its own URL, and fields left here "in case" would be
// populated by the handler, rendered by nothing, and asserted by a stub
// template — which is how TestTokenPageOffersEveryScope went on passing while
// no longer touching the page that renders the checkboxes.
type tokensData struct {
	PageData
	Tokens  []tokenRow
	Enabled bool

	// BaseURL is the instance's public address, for the endpoint line shown
	// beside a freshly created token. It comes from Base rather than from
	// PageData, which does not carry it — the same arrangement the waitlist and
	// form pages use for the endpoints they print.
	BaseURL string

	// NewToken is the raw token, rendered once immediately after creation and
	// never again.
	//
	// It is deliberately not carried in a flash. A flash is a signed cookie: the
	// value would be written to the browser's cookie jar, sent back on the next
	// request, and sit in whatever logs a proxy keeps of request headers. Held
	// in the response body of the POST that created it, it goes exactly one
	// place.
	NewToken string
}

// tokenFormData is the create form, which now lives on its own URL so it can be
// both an overlay and an ordinary page.
type tokenFormData struct {
	PageData
	Scopes  []scopeOption
	Error   string
	Enabled bool
	TTLDays int

	// Name and Ticked keep the operator's input when creation is refused, so a
	// rejected form does not also lose what they typed.
	Name   string
	Ticked map[string]bool

	// Forms are the forms this operator can bind a token to, and TickedForms is
	// which of them were chosen. AllForms is the radio's state: true is the
	// default, because a picker that starts at "none" mints a token that can
	// read nothing.
	Forms       []store.FormSummary
	TickedForms map[string]bool
	AllForms    bool
}

// scopeOptions builds the checkbox list from the package that owns the value
// set, rather than restating it in a template where it would fall behind.
func scopeOptions() []scopeOption {
	out := make([]scopeOption, 0, len(mcpserver.AllScopes))
	for _, s := range mcpserver.AllScopes {
		out = append(out, scopeOption{
			Value: string(s), Description: s.Describe(), Caution: s.Caution(),
		})
	}
	return out
}

// Page renders the token list.
func (h *TokensHandler) Page(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "")
}

// NewPage serves the create form in both of its presentations.
//
// app.js turns the "New token" link into the drawer by re-fetching this same URL
// with X-Fragment; without JavaScript, or when the link is opened directly or
// shared, the identical form renders as an ordinary page. This is the mechanism
// the submission reader already uses — the overlay is an enhancement over markup
// that works without it, never the only way in.
// A fresh form starts at read and nothing else. The scope set is the only
// control that bounds what a client can do with a token, and a form that opens
// with nothing ticked makes "tick all three" the path of least resistance —
// which is how delete ends up on a token that only ever needed to list an
// inbox. It applies to the untouched form only: once an operator has chosen,
// the rejected-form path below re-renders their actual choice, because
// re-ticking a box they cleared would be the handler overruling them.
func (h *TokensHandler) NewPage(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, "", "", []string{string(mcpserver.ScopeRead)}, nil, true)
}

// renderForm draws the create form, as a fragment when the drawer asked for it
// and as a page otherwise.
//
// Both presentations are defined in one template and share one body, so they
// cannot drift into offering different scopes.
func (h *TokensHandler) renderForm(w http.ResponseWriter, r *http.Request, errMsg, name string, ticked, tickedForms []string, allForms bool) {
	data := tokenFormData{
		PageData:    h.Shell(w, r, "New API token", "tokens"),
		Scopes:      scopeOptions(),
		Error:       errMsg,
		Enabled:     h.MCPEnabled,
		TTLDays:     h.TTLDays,
		Name:        name,
		Ticked:      map[string]bool{},
		TickedForms: map[string]bool{},
		AllForms:    allForms,
	}
	for _, s := range ticked {
		data.Ticked[s] = true
	}
	for _, f := range tickedForms {
		data.TickedForms[f] = true
	}

	// A failure here loses the picker, not the page: the operator can still
	// mint an all-forms token, which is what they could do before this existed.
	forms, err := h.Store.ListForms(store.AllForms())
	if err != nil {
		log.Printf("tokens: listing forms for the picker: %v", err)
		data.Degraded = true
	}
	data.Forms = forms

	if r.Header.Get("X-Fragment") != "" {
		tmpl := h.Templates["token_new.html"]
		if err := tmpl.ExecuteTemplate(w, "drawer", data); err != nil {
			log.Printf("token form drawer template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	h.Render(w, "token_new.html", data)
}

// render draws the list. Every exit that shows it goes through here, so the list
// is always rebuilt from storage and never rendered stale.
//
// newToken is the raw value to show once, and only ever arrives from Create.
func (h *TokensHandler) render(w http.ResponseWriter, r *http.Request, newToken string) {
	user, _ := auth.UserFromContext(r.Context())

	data := tokensData{
		PageData: h.Shell(w, r, "API tokens", "tokens"),
		Enabled:  h.MCPEnabled,
		BaseURL:  h.Base.BaseURL,
		NewToken: newToken,
	}

	tokens, err := h.Store.ListAPITokens(user.ID)
	if err != nil {
		// Degraded rather than 500: the create form still works, and the banner
		// says the list could not be read so an empty table is not mistaken for
		// "you have no tokens" — which would invite creating a duplicate of one
		// that already exists.
		log.Printf("tokens: list for %s: %v", user.ID, err)
		data.Degraded = true
	}
	// Form names for the reach column. A failure degrades that one column to
	// ids rather than the page, since an id is still an answer.
	names := map[string]string{}
	if forms, err := h.Store.ListForms(store.AllForms()); err != nil {
		log.Printf("tokens: naming the forms tokens reach: %v", err)
		data.Degraded = true
	} else {
		for _, f := range forms {
			names[f.ID] = f.Name
		}
	}

	for _, t := range tokens {
		data.Tokens = append(data.Tokens, tokenRow{
			APIToken:  t,
			ScopeList: strings.Join(t.Scopes, ", "),
			Reach:     describeReach(t, names),
			LastUsed:  humanTime(t.LastUsedAt, "Never"),
			Expires:   humanTime(t.ExpiresAt, "Never"),
		})
	}

	if err := h.Templates["tokens.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("tokens template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// humanTime renders a timestamp for the table, or a word for the zero time.
//
// The fallback is a parameter rather than a hardcoded dash because "Never" means
// two different true things in this table — never used, never expires — and a
// dash would make both read as missing data.
func humanTime(t time.Time, zero string) string {
	if t.IsZero() {
		return zero
	}
	return t.Format("Jan 2, 2006 15:04")
}

// Create mints a token and renders it exactly once.
//
// It answers 200 with the page rather than redirecting, which is the one place
// this screen departs from the POST-redirect-GET the rest of the admin uses. It
// has to: the raw token exists only in this response, and a redirect would need
// to carry it through a cookie or a query string — both of which write the
// credential somewhere it would outlive the page.
//
// The cost is a re-POST on refresh, which mints a second token rather than
// repeating the first. That is visible (it appears in the list, named the same)
// and harmless (it is revocable), which is the better failure of the two.
func (h *TokensHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	ticked := r.Form["scopes"]
	tickedForms := r.Form["form_ids"]

	// Ticking a form binds the token, whatever the radio says.
	//
	// The radio and the checkboxes can disagree — there is no JavaScript
	// coupling them, and a browser submits boxes ticked before the radio moved.
	// Reading the radio alone meant a person who ticked "Careers" and forgot to
	// move it got a token reaching everything, which is the one direction this
	// must not fail in: granting more than was asked for, silently. Binding
	// instead can only grant less than intended, which the list shows and a
	// revoke undoes.
	allForms := r.FormValue("reach") != "listed" && len(tickedForms) == 0

	// A refusal comes back on the form, carrying what was typed. Sending someone
	// to the list on a typo drops them somewhere the form is not.
	fail := func(msg string) {
		h.renderForm(w, r, msg, name, ticked, tickedForms, allForms)
	}

	if name == "" {
		fail("Give the token a name, so you can tell it apart from the others later.")
		return
	}
	if len(name) > 64 {
		fail("That name is too long — 64 characters at most.")
		return
	}

	// ValidateScopes rather than ParseScopes: creating a token is a person
	// stating an intent, and silently granting them less than they ticked is how
	// someone spends an hour debugging a refusal the form could have named here.
	scopes, err := mcpserver.ValidateScopes(ticked)
	if err != nil {
		fail(capitalise(err.Error()) + ".")
		return
	}

	var formIDs []string
	if !allForms {
		var err error
		if formIDs, err = h.validateForms(tickedForms); err != nil {
			fail(capitalise(err.Error()) + ".")
			return
		}
	}

	var expiry time.Duration
	if h.TTLDays > 0 {
		expiry = time.Duration(h.TTLDays) * 24 * time.Hour
	}

	raw, tok, err := h.Store.CreateAPIToken(user.ID, name, scopes.Strings(), formIDs, expiry)
	if err != nil {
		log.Printf("tokens: create for %s: %v", user.ID, err)
		fail("That token could not be created.")
		return
	}
	// The id and the scopes, never the value. This line is the reason the value
	// is a local and not a field on anything.
	log.Printf("tokens: created %s (%q, scopes %s, %s) for user %s",
		tok.ID, tok.Name, scopes, tok.Scope(), user.Username)

	h.render(w, r, raw)
}

// describeReach says what a token can see, in form names.
//
// A form that has since been deleted keeps its id here rather than vanishing:
// the token still names it, and a reach that silently shortened would tell an
// operator the token is narrower than it is.
func describeReach(t store.APIToken, names map[string]string) string {
	if t.Scope().All() {
		return "All forms"
	}
	out := make([]string, 0, len(t.FormIDs))
	for _, id := range t.FormIDs {
		if name, ok := names[id]; ok {
			out = append(out, name)
			continue
		}
		out = append(out, id)
	}
	return strings.Join(out, ", ")
}

// validateForms checks the ids a person ticked against the forms that exist.
//
// Named rather than dropped, for the reason ValidateScopes gives: these arrive
// from a person stating an intent, and silently discarding one produces a token
// that reaches less than they asked for — or, if all of them go, nothing at
// all, which would look like the feature is broken rather than like a typo.
func (h *TokensHandler) validateForms(ids []string) ([]string, error) {
	forms, err := h.Store.ListForms(store.AllForms())
	if err != nil {
		log.Printf("tokens: listing forms to validate a token's reach: %v", err)
		return nil, fmt.Errorf("the list of forms could not be read, so this token cannot be limited to one")
	}
	known := make(map[string]bool, len(forms))
	for _, f := range forms {
		known[f.ID] = true
	}

	out := make([]string, 0, len(ids))
	var unknown []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		switch {
		case id == "":
		case known[id]:
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		default:
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("no form with id %s", strings.Join(unknown, ", "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("choose at least one form, or give the token every form")
	}
	return out, nil
}

// Delete revokes one of the signed-in user's own tokens.
//
// Scoped by owner in the store, not here: the id arrives from a form post, and
// an id-only delete would revoke another user's credential and flash success.
func (h *TokensHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	removed, err := h.Store.DeleteAPIToken(user.ID, id)
	switch {
	case err != nil:
		log.Printf("tokens: delete %s for %s: %v", id, user.ID, err)
		flash.Set(w, h.SecretKey, "error", "That token could not be revoked.")
	case !removed:
		// Not an error. A double-submitted form, or a token another session
		// already revoked, both land here — and saying "could not be revoked"
		// about a token that is already gone sends an operator looking for it.
		flash.Set(w, h.SecretKey, "success", "That token is already gone.")
	default:
		log.Printf("tokens: revoked %s for user %s", id, user.Username)
		flash.Set(w, h.SecretKey, "success", "Token revoked. Any client using it is refused from now on.")
	}
	http.Redirect(w, r, "/admin/tokens", http.StatusSeeOther)
}

// capitalise uppercases the first letter, so an error written for a log reads as
// a sentence on the page.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
