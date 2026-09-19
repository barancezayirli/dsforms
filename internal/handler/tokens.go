package handler

import (
	"log"
	"net/http"
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
	CreateAPIToken(userID, name string, scopes []string, expiry time.Duration) (string, store.APIToken, error)
	DeleteAPIToken(userID, id string) (bool, error)
	ListAPITokens(userID string) ([]store.APIToken, error)
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
}

// scopeOption is one checkbox on the create form.
type scopeOption struct {
	Value       string
	Description string
}

type tokensData struct {
	PageData
	Tokens  []tokenRow
	Scopes  []scopeOption
	Error   string
	Enabled bool
	TTLDays int

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

	// FormName keeps the typed name on the page when creation is refused, so a
	// rejected form does not also lose the input.
	FormName string
	// FormScopes are the boxes that were ticked, same reason.
	FormScopes map[string]bool
}

// scopeOptions builds the checkbox list from the package that owns the value
// set, rather than restating it in a template where it would fall behind.
func scopeOptions() []scopeOption {
	out := make([]scopeOption, 0, len(mcpserver.AllScopes))
	for _, s := range mcpserver.AllScopes {
		out = append(out, scopeOption{Value: string(s), Description: s.Describe()})
	}
	return out
}

// renderOpts is what varies between this page's three outcomes: a plain view, a
// refused creation, and a successful one.
//
// A struct rather than four string parameters, because three of the four are
// strings and a call site that swaps two of them compiles cleanly — which for
// this page would mean rendering the error message where the token goes.
type renderOpts struct {
	// NewToken is the raw value, shown once and never again.
	NewToken string
	// Err re-renders the form with a message above it.
	Err string
	// Name and Ticked keep the operator's input on a refused form.
	Name   string
	Ticked []string
}

// Page renders the token list and the create form.
func (h *TokensHandler) Page(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, renderOpts{})
}

// render draws the page. Every exit from this handler goes through it, so the
// list is always rebuilt from storage and an error never renders a stale one.
func (h *TokensHandler) render(w http.ResponseWriter, r *http.Request, opts renderOpts) {
	user, _ := auth.UserFromContext(r.Context())

	data := tokensData{
		PageData:   h.Shell(w, r, "API tokens", "tokens"),
		Scopes:     scopeOptions(),
		Error:      opts.Err,
		Enabled:    h.MCPEnabled,
		TTLDays:    h.TTLDays,
		BaseURL:    h.Base.BaseURL,
		NewToken:   opts.NewToken,
		FormName:   opts.Name,
		FormScopes: map[string]bool{},
	}
	for _, s := range opts.Ticked {
		data.FormScopes[s] = true
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
	for _, t := range tokens {
		data.Tokens = append(data.Tokens, tokenRow{
			APIToken:  t,
			ScopeList: strings.Join(t.Scopes, ", "),
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

	fail := func(msg string) {
		h.render(w, r, renderOpts{Err: msg, Name: name, Ticked: ticked})
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

	var expiry time.Duration
	if h.TTLDays > 0 {
		expiry = time.Duration(h.TTLDays) * 24 * time.Hour
	}

	raw, tok, err := h.Store.CreateAPIToken(user.ID, name, scopes.Strings(), expiry)
	if err != nil {
		log.Printf("tokens: create for %s: %v", user.ID, err)
		fail("That token could not be created.")
		return
	}
	// The id and the scopes, never the value. This line is the reason the value
	// is a local and not a field on anything.
	log.Printf("tokens: created %s (%q, scopes %s) for user %s", tok.ID, tok.Name, scopes, user.Username)

	h.render(w, r, renderOpts{NewToken: raw})
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
