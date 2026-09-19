package handler

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// Populated enough to take every branch: a zero-valued fixture skips each
	// {{if}} and {{range}} body, which is how a template test passes while most
	// of the template never runs.
	template.Must(tok.New("content").Parse(
		`{{if .Error}}<p class="error">{{.Error}}</p>{{end}}` +
			`{{if .NewToken}}<code id="new-token">{{.NewToken}}</code>{{end}}` +
			`{{range .Tokens}}<span class="token" data-id="{{.ID}}">{{.Name}}:{{.ScopeList}}:{{.LastUsed}}</span>{{end}}` +
			`{{range .Scopes}}<label class="scope">{{.Value}} {{.Description}}</label>{{end}}`))
	templates := map[string]*template.Template{"tokens.html": tok}

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
		r.Post("/admin/tokens", th.Create)
		r.Post("/admin/tokens/{id}/delete", th.Delete)
	})
	return s, r
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
	if _, _, err := s.CreateAPIToken(colleague.ID, "their-laptop", []string{"read"}, 0); err != nil {
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
	raw, tok, err := s.CreateAPIToken(admin.ID, "mine", []string{"read"}, 0)
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
	raw, tok, err := s.CreateAPIToken(victim.ID, "theirs", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// admin is signed in and posts the victim's token id.
	doTokenRequest(t, s, r, "POST", "/admin/tokens/"+tok.ID+"/delete", "")

	if _, err := s.GetAPIToken(raw); err != nil {
		t.Errorf("another user's token was revoked through the admin page: %v", err)
	}
}

// TestTokenPageOffersEveryScope. The checkboxes are built from
// mcpserver.AllScopes rather than written into the template, so a scope added to
// that package cannot go unofferable.
func TestTokenPageOffersEveryScope(t *testing.T) {
	t.Parallel()
	s, r := setupTokens(t)
	body := doTokenRequest(t, s, r, "GET", "/admin/tokens", "").Body.String()

	for _, scope := range mcpserver.AllScopes {
		if !strings.Contains(body, string(scope)) {
			t.Errorf("scope %q is not offered on the page", scope)
		}
		if !strings.Contains(body, scope.Describe()) {
			t.Errorf("scope %q is offered with no description beside it", scope)
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
	_, unused, err := s.CreateAPIToken(admin.ID, "unused", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	_, used, err := s.CreateAPIToken(admin.ID, "used", []string{"read", "delete"}, 0)
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
