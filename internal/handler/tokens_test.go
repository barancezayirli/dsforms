package handler

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/mcpserver"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

func setupTokens(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}

	baseTmpl := template.Must(template.New("base").Funcs(TemplateFuncs()).
		Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	tok, _ := baseTmpl.Clone()
	// The list stub renders only what the list page renders. It deliberately
	// does not range .Scopes: a stub that shows more than the real template can
	// satisfy an assertion the shipped page would fail.
	template.Must(tok.New("content").Parse(
		`{{if .NewToken}}<code id="new-token">{{.NewToken}}</code>{{end}}` +
			`{{range .Tokens}}<span class="token" data-id="{{.ID}}">{{.Name}}:{{.ScopeList}}:{{.LastUsed}}</span>{{end}}`))
	nw, _ := baseTmpl.Clone()
	template.Must(nw.New("content").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}` +
			`<form id="page-form" value="{{.Name}}">` +
			`{{range .Scopes}}<label class="scope">{{.Value}} {{.Description}}</label>{{end}}</form>`))
	template.Must(nw.New("drawer").Parse(
		`<div class="backdrop"></div><div class="drawer" role="dialog">` +
			`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}` +
			`<form id="drawer-form">` +
			`{{range .Scopes}}<label class="scope">{{.Value}}</label>{{end}}</form></div>`))

	templates := map[string]*template.Template{"tokens.html": tok, "token_new.html": nw}

	th := &TokensHandler{
		Store:   s,
		TTLDays: 0,
		Base: Base{
			Nav:       s,
			SecretKey: testSecretKey,
			BaseURL:   "https://example.com",
			Templates: templates,
		},
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/tokens", th.Page)
		r.Get("/admin/tokens/new", th.NewPage)
		r.Post("/admin/tokens", th.Create)
		r.Post("/admin/tokens/{id}/delete", th.Delete)
	})
	return s, r
}

// setupTokensRealForm is setupTokens with the shipped token_new.html in place
// of the stub, for the assertions that are about the page an operator sees
// rather than about what the handler put in a struct.
func setupTokensRealForm(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s2, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	th := &TokensHandler{
		Store: s2, TTLDays: 0,
		Base: Base{
			Nav: s2, SecretKey: testSecretKey, BaseURL: "https://example.com",
			Templates: map[string]*template.Template{
				"tokens.html":    realTemplates(t)["tokens.html"],
				"token_new.html": realTemplates(t)["token_new.html"],
			},
		},
	}
	mux := chi.NewRouter()
	mux.Group(func(rt chi.Router) {
		rt.Use(auth.RequireAuth(s2))
		rt.Get("/admin/tokens", th.Page)
		rt.Get("/admin/tokens/new", th.NewPage)
		rt.Post("/admin/tokens", th.Create)
	})
	return s2, mux
}

func doTokenRequest(t *testing.T, s *store.Store, r *chi.Mux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doTokenRequestAs(t, s, r, "admin", method, path, body)
}

func doTokenRequestAs(t *testing.T, s *store.Store, r *chi.Mux, username, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	u, err := s.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("GetUserByUsername(%s): %v", username, err)
	}
	token, err := s.CreateSession(u.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(auth.CreateSessionCookie(token, "https://example.com"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestCreateTokenShowsTheValueExactlyOnce is the whole contract of this page.
//
// The raw token has to reach the operator, and it has to be unrecoverable
// afterwards. A page that showed it again on reload would make the database's
// hash-only storage pointless, and one that never showed it would be unusable.
func TestCreateTokenShowsTheValueExactlyOnce(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	form := url.Values{"name": {"laptop"}, "scopes": {"read", "write"}}
	w := doTokenRequest(t, s, r, "POST", "/admin/tokens", form.Encode())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the token is rendered, not redirected to", w.Code)
	}

	body := w.Body.String()
	start := strings.Index(body, `<code id="new-token">`)
	if start < 0 {
		t.Fatalf("the created token was not rendered: %.300q", body)
	}
	raw := body[start+len(`<code id="new-token">`):]
	raw = raw[:strings.Index(raw, "</code>")]
	if !strings.HasPrefix(raw, store.APITokenPrefix) {
		t.Fatalf("rendered token %q does not look like one", raw)
	}

	// It works.
	got, err := s.GetAPIToken(raw)
	if err != nil {
		t.Fatalf("the rendered token does not authenticate: %v", err)
	}
	if got.Name != "laptop" {
		t.Errorf("Name = %q, want laptop", got.Name)
	}

	// And it is never shown again.
	again := doTokenRequest(t, s, r, "GET", "/admin/tokens", "")
	if strings.Contains(again.Body.String(), raw) {
		t.Error("the token is shown again on a later page load; it must be unrecoverable after creation")
	}
	if !strings.Contains(again.Body.String(), "laptop") {
		t.Error("the token is not listed at all after creation")
	}
}

// TestCreateTokenRejectsBadInput. Every refusal re-renders the page with a
// message rather than 500ing or silently creating something different.
func TestCreateTokenRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		form    url.Values
		wantErr string
	}{
		{"no name", url.Values{"scopes": {"read"}}, "name"},
		{"blank name", url.Values{"name": {"   "}, "scopes": {"read"}}, "name"},
		{"no scopes", url.Values{"name": {"x"}}, "scope"},
		{"unknown scope", url.Values{"name": {"x"}, "scopes": {"superuser"}}, "superuser"},
		{"one good one bad", url.Values{"name": {"x"}, "scopes": {"read", "wirte"}}, "wirte"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, r := setupTokens(t)
			w := doTokenRequest(t, s, r, "POST", "/admin/tokens", tt.form.Encode())

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 with an error on the page", w.Code)
			}
			body := w.Body.String()
			if !strings.Contains(body, `class="error"`) {
				t.Fatalf("no error rendered for %v: %.300q", tt.form, body)
			}
			if !strings.Contains(body, tt.wantErr) {
				t.Errorf("the error does not mention %q: %.300q", tt.wantErr, body)
			}
			if strings.Contains(body, `id="new-token"`) {
				t.Error("a token was created despite the error")
			}

			admin, _ := s.GetUserByUsername("admin")
			tokens, err := s.ListAPITokens(admin.ID)
			if err != nil {
				t.Fatalf("ListAPITokens: %v", err)
			}
			if len(tokens) != 0 {
				t.Errorf("%d tokens exist after a refused creation", len(tokens))
			}
		})
	}
}

// TestTokenPageOnlyListsYourOwnTokens. Tokens are per-user credentials, and
// their names and scopes describe what someone else's automation can do.
func TestTokenPageOnlyListsYourOwnTokens(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	if err := s.CreateUser("colleague", "hunter2hunter2"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	colleague, _ := s.GetUserByUsername("colleague")
	if _, _, err := s.CreateAPIToken(colleague.ID, "their-laptop", []string{"read"}, nil, 0); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	doTokenRequest(t, s, r, "POST", "/admin/tokens", url.Values{"name": {"my-laptop"}, "scopes": {"read"}}.Encode())

	w := doTokenRequest(t, s, r, "GET", "/admin/tokens", "")
	body := w.Body.String()
	if !strings.Contains(body, "my-laptop") {
		t.Error("your own token is not listed")
	}
	if strings.Contains(body, "their-laptop") {
		t.Error("another user's token is listed")
	}
}

// TestDeleteTokenRevokesYourOwnAndNotAnothersSuccess is the authorisation test
// for the delete. The id comes from a form post, so an id-only delete would let
// anyone with a session revoke anyone's credential.
func TestDeleteTokenRevokesYourOwn(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	admin, _ := s.GetUserByUsername("admin")
	raw, tok, err := s.CreateAPIToken(admin.ID, "mine", []string{"read"}, nil, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	w := doTokenRequest(t, s, r, "POST", "/admin/tokens/"+tok.ID+"/delete", "")
	if w.Code != http.StatusSeeOther && w.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect back to the page", w.Code)
	}
	if _, err := s.GetAPIToken(raw); err == nil {
		t.Error("the token still authenticates after being revoked")
	}
}

func TestDeleteTokenCannotRevokeAnothersToken(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	if err := s.CreateUser("victim", "hunter2hunter2"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	victim, _ := s.GetUserByUsername("victim")
	raw, tok, err := s.CreateAPIToken(victim.ID, "theirs", []string{"read"}, nil, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// admin is signed in and posts the victim's token id.
	doTokenRequest(t, s, r, "POST", "/admin/tokens/"+tok.ID+"/delete", "")

	if _, err := s.GetAPIToken(raw); err != nil {
		t.Errorf("another user's token was revoked through the admin page: %v", err)
	}
}

// TestTokenFormOffersEveryScope renders the *shipped* token_new.html, not the
// stub the rest of this file uses.
//
// That distinction is the whole test. The original version asked the handler
// for the page and asserted on what came back — which is a stub written here,
// so it was really asserting that this file ranges .Scopes. It passed after the
// form moved to another page entirely, and it still passed when the real
// template was edited to offer one scope out of three. A guard nobody has
// watched fail is not a guard: this one now fails under both.
func TestTokenFormOffersEveryScope(t *testing.T) {
	t.Parallel()

	tmpl, ok := realTemplates(t)["token_new.html"]
	if !ok {
		t.Fatal("token_new.html is not in basePageNames")
	}

	data := tokenFormData{
		PageData: PageData{Title: "New API token", Active: "tokens"},
		Scopes:   scopeOptions(),
		Ticked:   map[string]bool{},
	}

	// Both presentations, because they are separate defines and only one of them
	// is what an operator with JavaScript actually sees.
	for _, block := range []string{"base", "drawer"} {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, block, data); err != nil {
			t.Fatalf("executing %s: %v", block, err)
		}
		body := buf.String()
		for _, scope := range mcpserver.AllScopes {
			if !strings.Contains(body, `value="`+string(scope)+`"`) {
				t.Errorf("%s: scope %q has no checkbox", block, scope)
			}
			if !strings.Contains(body, scope.Describe()) {
				t.Errorf("%s: scope %q is offered with no description beside it", block, scope)
			}
		}
	}
}

// TestTokenListReportsUseAndScopes. The list is what an operator reads while
// deciding whether a token is still needed, so "never used" has to be
// distinguishable from "used", and the scopes have to be visible.
func TestTokenListReportsUseAndScopes(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	admin, _ := s.GetUserByUsername("admin")
	_, unused, err := s.CreateAPIToken(admin.ID, "unused", []string{"read"}, nil, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	_, used, err := s.CreateAPIToken(admin.ID, "used", []string{"read", "delete"}, nil, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := s.TouchAPIToken(used.ID); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}

	body := doTokenRequest(t, s, r, "GET", "/admin/tokens", "").Body.String()

	if !strings.Contains(body, "unused:read:Never") {
		t.Errorf("an unused token does not say so: %.400q", body)
	}
	if strings.Contains(body, "used:read, delete:Never") {
		t.Error("a used token is reported as never used")
	}
	if !strings.Contains(body, "used:read, delete:") {
		t.Errorf("the token's scopes are not listed: %.400q", body)
	}
	_ = unused
}

// TestNewTokenPageServesBothPresentations is the progressive-enhancement
// contract this screen now rests on.
//
// The "New token" control is a real link. app.js turns it into the drawer by
// re-fetching the same URL with X-Fragment; with JavaScript off, or when the
// link is opened directly or shared, the identical form has to render as an
// ordinary page. One of the two missing means the button either does nothing
// or opens a full document inside an overlay.
func TestNewTokenPageServesBothPresentations(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	t.Run("full page without the header", func(t *testing.T) {
		w := doTokenRequest(t, s, r, "GET", "/admin/tokens/new", "")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, `id="page-form"`) {
			t.Errorf("no page form rendered: %.200q", body)
		}
		if strings.Contains(body, `class="drawer"`) {
			t.Error("the drawer fragment was served for an ordinary page load")
		}
	})

	t.Run("fragment with X-Fragment", func(t *testing.T) {
		admin, _ := s.GetUserByUsername("admin")
		token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
		req := httptest.NewRequest("GET", "/admin/tokens/new", nil)
		req.AddCookie(auth.CreateSessionCookie(token, "https://example.com"))
		req.Header.Set("X-Fragment", "1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, `class="drawer"`) || !strings.Contains(body, `id="drawer-form"`) {
			t.Fatalf("no drawer fragment rendered: %.200q", body)
		}
		if strings.Contains(body, `id="page-form"`) {
			t.Error("the full page was served into the drawer")
		}
	})

	t.Run("both offer every scope", func(t *testing.T) {
		// The two presentations share one scope list, so neither can quietly
		// offer fewer powers than the other.
		for _, scope := range mcpserver.AllScopes {
			page := doTokenRequest(t, s, r, "GET", "/admin/tokens/new", "").Body.String()
			if !strings.Contains(page, string(scope)) {
				t.Errorf("the page form does not offer %q", scope)
			}
		}
	})
}

// TestNewTokenPageRequiresASession. It is a new route outside the walk that
// routes_test.go does for /admin, so it gets its own check here too.
func TestNewTokenPageRequiresASession(t *testing.T) {
	t.Parallel()
	_, r := setupTokens(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/tokens/new", nil))
	if w.Code != http.StatusFound && w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d without a session, want a refusal", w.Code)
	}
}

// TestRefusedCreationComesBackOnTheForm.
//
// A rejected create used to re-render the token *list*. Now that the form lives
// in a drawer, sending someone to the list on a typo drops them somewhere the
// form is not, with their input gone. The refusal has to land back on the form,
// carrying what they typed.
func TestRefusedCreationComesBackOnTheForm(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)

	form := url.Values{"name": {"laptop"}, "scopes": {"wirte"}}
	w := doTokenRequest(t, s, r, "POST", "/admin/tokens", form.Encode())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="page-form"`) {
		t.Fatalf("a refused creation did not come back on the form: %.300q", body)
	}
	if !strings.Contains(body, "wirte") {
		t.Errorf("the error does not name the bad scope: %.300q", body)
	}
	if !strings.Contains(body, `value="laptop"`) {
		t.Errorf("the typed name was lost on the way back: %.300q", body)
	}
}

// TestANewTokenFormStartsAtReadOnly. The scope set is the only control that
// bounds what a compromised or careless client can do, and a form that starts
// with nothing ticked makes "tick all three" the path of least resistance.
func TestANewTokenFormStartsAtReadOnly(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/tokens/new", nil)
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/tokens/new = %d, want 200", rec.Code)
	}

	ticked := tickedScopes(t, rec.Body.String())
	if !ticked["read"] {
		t.Error("read is not ticked on a fresh form")
	}
	for _, sc := range mcpserver.AllScopes {
		if sc == mcpserver.ScopeRead {
			continue
		}
		if ticked[string(sc)] {
			t.Errorf("%s is ticked on a fresh form — the default must be the least a "+
				"client can be given, not the most", sc)
		}
	}
}

// TestARejectedFormKeepsWhatWasActuallyTicked. The default applies to a form
// nobody has filled in. Once an operator has chosen, re-rendering their choice
// as the default would silently re-tick a box they cleared.
func TestARejectedFormKeepsWhatWasActuallyTicked(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)

	form := url.Values{"name": {""}, "scopes": {"delete"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	ticked := tickedScopes(t, rec.Body.String())
	if ticked["read"] {
		t.Error("read was re-ticked on a rejected form the operator had left unticked")
	}
	if !ticked["delete"] {
		t.Error("the operator's own choice was lost")
	}
}

// tickedScopes reads the rendered form rather than the struct behind it. What
// an operator gets is the checkbox, and a Ticked map that never reaches a
// checked attribute is a default nobody receives.
var checkboxRE = regexp.MustCompile(`(?s)<input type="checkbox" name="scopes" value="([a-z]+)"(.*?)>`)

func tickedScopes(t *testing.T, body string) map[string]bool {
	t.Helper()
	matches := checkboxRE.FindAllStringSubmatch(body, -1)
	if len(matches) != len(mcpserver.AllScopes) {
		t.Fatalf("found %d scope checkboxes, want %d — the form is not what this test thinks",
			len(matches), len(mcpserver.AllScopes))
	}
	out := map[string]bool{}
	for _, m := range matches {
		out[m[1]] = strings.Contains(m[2], "checked")
	}
	return out
}

// TestTokenFormStatesWhatEachScopeRisks renders the shipped template, for the
// reason TestTokenFormOffersEveryScope gives: a stub agrees with the handler by
// construction.
func TestTokenFormStatesWhatEachScopeRisks(t *testing.T) {
	t.Parallel()

	tmpl := realTemplates(t)["token_new.html"]
	data := tokenFormData{
		PageData: PageData{Title: "New API token", Active: "tokens"},
		Scopes:   scopeOptions(),
		Ticked:   map[string]bool{"read": true},
	}

	for _, block := range []string{"base", "drawer"} {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, block, data); err != nil {
			t.Fatalf("executing %s: %v", block, err)
		}
		body := buf.String()
		for _, scope := range mcpserver.AllScopes {
			if !strings.Contains(body, template.HTMLEscapeString(scope.Caution())) {
				t.Errorf("%s: scope %q is offered without saying what it risks", block, scope)
			}
		}
	}
}

// seedForms gives the token form something to offer.
func seedTokenForms(t *testing.T, s *store.Store) {
	t.Helper()
	for _, f := range []store.Form{
		{ID: "f1", Name: "Contact", EmailTo: "me@example.com"},
		{ID: "f2", Name: "Careers", EmailTo: "jobs@example.com"},
	} {
		if err := s.CreateForm(f); err != nil {
			t.Fatalf("CreateForm(%s): %v", f.ID, err)
		}
	}
}

// TestATokenCanBeBoundToForms is the point of the whole feature reaching the
// operator: if the picker does not work, the control does not exist.
func TestATokenCanBeBoundToForms(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)
	seedTokenForms(t, s)

	form := url.Values{
		"name":     {"careers bot"},
		"scopes":   {"read"},
		"reach":    {"listed"},
		"form_ids": {"f2"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d, want 200", rec.Code)
	}

	admin, _ := s.GetUserByUsername("admin")
	tokens, err := s.ListAPITokens(admin.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens = %d, want 1", len(tokens))
	}
	scope := tokens[0].Scope()
	if scope.All() {
		t.Fatal("the token reaches every form despite naming one")
	}
	if !scope.Allows("f2") || scope.Allows("f1") {
		t.Errorf("scope = %v, want only f2", tokens[0].FormIDs)
	}
}

// TestChoosingEveryFormRecordsNoForms. "All forms" has to store nothing, not
// every id: a form added tomorrow would otherwise be outside a token the
// operator believed was unbounded.
func TestChoosingEveryFormRecordsNoForms(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)
	seedTokenForms(t, s)

	form := url.Values{"name": {"everything"}, "scopes": {"read"}, "reach": {"all"}, "form_ids": {"f1"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(loginCookie(t, s))
	r.ServeHTTP(httptest.NewRecorder(), req)

	admin, _ := s.GetUserByUsername("admin")
	tokens, _ := s.ListAPITokens(admin.ID)
	if len(tokens) != 1 {
		t.Fatalf("tokens = %d, want 1", len(tokens))
	}
	if len(tokens[0].FormIDs) != 0 {
		t.Errorf("FormIDs = %v, want none — a ticked box under an unchosen option "+
			"must not narrow a token the operator asked to be unbounded", tokens[0].FormIDs)
	}
	if !tokens[0].Scope().All() {
		t.Error("the token does not reach every form")
	}
}

// TestNamingNoFormsIsRefused. An empty set is a token that can read nothing,
// which is never what anyone means to create — the same reasoning
// ValidateScopes uses for a token with no scopes.
func TestNamingNoFormsIsRefused(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)
	seedTokenForms(t, s)

	form := url.Values{"name": {"nothing"}, "scopes": {"read"}, "reach": {"listed"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	admin, _ := s.GetUserByUsername("admin")
	tokens, _ := s.ListAPITokens(admin.ID)
	if len(tokens) != 0 {
		t.Fatalf("a token was created with no forms: %+v", tokens)
	}
	if !strings.Contains(rec.Body.String(), "form") {
		t.Errorf("the refusal does not mention forms:\n%s", rec.Body.String())
	}
}

// TestAnUnknownFormIsRefused. The ids come from a form post, so they are the
// operator's input rather than ours, and a typo must be named rather than
// silently producing a token that reaches nothing.
func TestAnUnknownFormIsRefused(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)
	seedTokenForms(t, s)

	form := url.Values{"name": {"typo"}, "scopes": {"read"}, "reach": {"listed"}, "form_ids": {"f2", "nope"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	admin, _ := s.GetUserByUsername("admin")
	if tokens, _ := s.ListAPITokens(admin.ID); len(tokens) != 0 {
		t.Fatalf("a token was created naming a form that does not exist: %+v", tokens)
	}
}

// TestTheTokenFormOffersEveryForm renders the shipped template, for the reason
// TestTokenFormOffersEveryScope gives.
func TestTheTokenFormOffersEveryForm(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)
	seedTokenForms(t, s)

	req := httptest.NewRequest(http.MethodGet, "/admin/tokens/new", nil)
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, want := range []string{`value="f1"`, `value="f2"`, "Contact", "Careers"} {
		if !strings.Contains(body, want) {
			t.Errorf("the form does not offer %q", want)
		}
	}
	// All forms is the default, because a picker defaulting to none would mint
	// a token that can read nothing. Read off the rendered radios rather than
	// matched as a literal, since attribute order is the template's business.
	checked := checkedRadios(t, body, "reach")
	if !checked["all"] {
		t.Error("All forms is not the default choice")
	}
	if checked["listed"] {
		t.Error("both reach options are checked")
	}
}

// TestTheTokenListShowsWhatEachTokenReaches. An operator deciding whether a
// token is still safe needs to see its bound without minting a new one.
func TestTheTokenListShowsWhatEachTokenReaches(t *testing.T) {
	t.Parallel()
	s, r := setupTokensRealForm(t)
	seedTokenForms(t, s)

	admin, _ := s.GetUserByUsername("admin")
	if _, _, err := s.CreateAPIToken(admin.ID, "bounded", []string{"read"}, []string{"f2"}, 0); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, _, err := s.CreateAPIToken(admin.ID, "unbounded", []string{"read"}, nil, 0); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/tokens", nil)
	req.AddCookie(loginCookie(t, s))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	body := rec.Body.String()

	// The bounded one names the form; the unbounded one says so.
	if !strings.Contains(body, "Careers") {
		t.Error("the bounded token does not say which form it reaches")
	}
	if !strings.Contains(body, "All forms") {
		t.Error("the unbounded token does not say it reaches all of them")
	}
}

// checkedRadios reads which options of a radio group the rendered page marks
// as chosen.
var radioRE = regexp.MustCompile(`(?s)<input type="radio" name="([a-z_]+)" value="([a-z]+)"(.*?)>`)

func checkedRadios(t *testing.T, body, group string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var found bool
	for _, m := range radioRE.FindAllStringSubmatch(body, -1) {
		if m[1] != group {
			continue
		}
		found = true
		out[m[2]] = strings.Contains(m[3], "checked")
	}
	if !found {
		t.Fatalf("no %q radios in the rendered page", group)
	}
	return out
}
