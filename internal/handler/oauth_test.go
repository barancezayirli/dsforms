package handler

import (
	"encoding/json"
	"errors"
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/oauth"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

const (
	oauthBase     = "https://forms.example.com"
	oauthRedirect = "https://client.example.com/cb"
	// RFC 7636 Appendix B, so the token step has a verifier that matches.
	pkceVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	pkceChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

type oauthEnv struct {
	s *store.Store
	r http.Handler
	// now is the consent signer's clock; tests move it to expire a page.
	now *time.Time
}

// consentTemplate parses the real consent page with the real partials, so the
// hidden fields these tests read back are the ones that ship.
func consentTemplate(t *testing.T) *template.Template {
	t.Helper()
	files := []string{filepath.Join(templateDir, "oauth_consent.html")}
	for _, p := range TemplatePartials {
		files = append(files, filepath.Join(templateDir, p))
	}
	tmpl, err := template.New("oauth_consent.html").Funcs(TemplateFuncs()).ParseFiles(files...)
	if err != nil {
		t.Fatalf("parse consent: %v", err)
	}
	return tmpl
}

func setupOAuth(t *testing.T) oauthEnv {
	return setupOAuthWith(t, func(s *store.Store) OAuthStore { return s })
}

// setupOAuthWith lets a test put a failing store between the handler and the
// real one, for the paths a healthy database never takes.
func setupOAuthWith(t *testing.T, wrap func(*store.Store) OAuthStore) oauthEnv {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	h := &OAuthHandler{
		Base: Base{
			BaseURL:   oauthBase,
			SecretKey: testSecretKey,
			Templates: map[string]*template.Template{"oauth_consent.html": consentTemplate(t)},
		},
		Store:   wrap(s),
		Consent: oauth.NewConsentSigner(testSecretKey, func() time.Time { return now }),
	}
	r := chi.NewRouter()
	h.Mount(r, auth.RequireAuth(s), func(next http.Handler) http.Handler { return next })
	return oauthEnv{s: s, r: r, now: &now}
}

// codes counts authorization codes ever issued, so a refusal can be shown to
// have issued nothing rather than merely to have rendered an error.
func (e oauthEnv) codes(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.s.DB().QueryRow("SELECT COUNT(*) FROM oauth_auth_codes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// failingStore wraps the real store and fails the methods a test names.
type failingStore struct {
	*store.Store
	getClient, exchange, refresh, listForms error
}

var errDatabase = errors.New("database is locked")

func (f failingStore) GetOAuthClient(id string) (store.OAuthClient, error) {
	if f.getClient != nil {
		return store.OAuthClient{}, f.getClient
	}
	return f.Store.GetOAuthClient(id)
}

func (f failingStore) ExchangeAuthCode(raw string, accept func(store.AuthCode) bool) (store.TokenPair, error) {
	if f.exchange != nil {
		return store.TokenPair{}, f.exchange
	}
	return f.Store.ExchangeAuthCode(raw, accept)
}

func (f failingStore) RefreshGrant(raw, clientID string) (store.TokenPair, error) {
	if f.refresh != nil {
		return store.TokenPair{}, f.refresh
	}
	return f.Store.RefreshGrant(raw, clientID)
}

func (f failingStore) ListForms(scope store.FormScope) ([]store.FormSummary, error) {
	if f.listForms != nil {
		return nil, f.listForms
	}
	return f.Store.ListForms(scope)
}

func (e oauthEnv) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func (e oauthEnv) cookie(t *testing.T, username string) *http.Cookie {
	t.Helper()
	u, err := e.s.GetUserByUsername(username)
	if err != nil {
		if err := e.s.CreateUser(username, "hunter2hunter2"); err != nil {
			t.Fatal(err)
		}
		u, _ = e.s.GetUserByUsername(username)
	}
	tok, err := e.s.CreateSession(u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return auth.CreateSessionCookie(tok, oauthBase)
}

func (e oauthEnv) register(t *testing.T, body string) map[string]any {
	t.Helper()
	w := e.do(t, httptest.NewRequest("POST", oauth.PathRegister, strings.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e oauthEnv) client(t *testing.T) string {
	t.Helper()
	return e.register(t, `{"client_name":"Claude","redirect_uris":["`+oauthRedirect+`","https://client.example.com/other"]}`)["client_id"].(string)
}

func authorizeQuery(clientID string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {oauthRedirect},
		"state":                 {"st&ate"},
		"code_challenge":        {pkceChallenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"read write"},
		"resource":              {oauthBase + "/mcp"},
	}
}

var hiddenField = regexp.MustCompile(`<input type="hidden" name="([a-z_]+)" value="([^"]*)">`)

// consentForm loads the consent page and returns the form it would post.
func (e oauthEnv) consentForm(t *testing.T, c *http.Cookie, q url.Values) url.Values {
	t.Helper()
	req := httptest.NewRequest("GET", oauth.PathAuthorize+"?"+q.Encode(), nil)
	req.AddCookie(c)
	w := e.do(t, req)
	if w.Code != http.StatusOK {
		t.Fatalf("consent page status = %d, Location %q, body %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	form := url.Values{}
	for _, m := range hiddenField.FindAllStringSubmatch(w.Body.String(), -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	if form.Get("consent") == "" {
		t.Fatalf("no consent signature on the page: %s", w.Body)
	}
	return form
}

func (e oauthEnv) postConsent(t *testing.T, c *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", oauth.PathAuthorize, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	return e.do(t, req)
}

// approve walks consent and returns the authorization code.
func (e oauthEnv) approve(t *testing.T, clientID string) string {
	t.Helper()
	c := e.cookie(t, "admin")
	form := e.consentForm(t, c, authorizeQuery(clientID))
	form.Set("action", "approve")
	form["scopes"] = []string{"read", "write"}
	form.Set("reach", "all")
	w := e.postConsent(t, c, form)
	loc := redirectTo(t, w)
	if loc.Query().Get("code") == "" {
		t.Fatalf("no code in %s", loc)
	}
	return loc.Query().Get("code")
}

func redirectTo(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e oauthEnv) token(t *testing.T, form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", oauth.PathToken, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := e.do(t, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func codeExchange(clientID, code string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {oauthRedirect},
		"client_id":     {clientID},
		"code_verifier": {pkceVerifier},
		"resource":      {oauthBase + "/mcp"},
	}
}

// --- discovery ---

func TestOAuthMetadataEndpoints(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	w := e.do(t, httptest.NewRequest("GET", oauth.PathServerMetadata, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("server metadata status = %d", w.Code)
	}
	var meta map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &meta)
	if meta["issuer"] != oauthBase || meta["token_endpoint"] != oauthBase+oauth.PathToken {
		t.Errorf("server metadata = %v", meta)
	}
	// Browser-based clients (the MCP Inspector, for one) read these cross-origin.
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("server metadata has no CORS header")
	}

	for _, path := range []string{oauth.PathResourceMetadata, oauth.PathResourceMetadata + "/mcp"} {
		w := e.do(t, httptest.NewRequest("GET", path, nil))
		var prm map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &prm)
		if w.Code != http.StatusOK || prm["resource"] != oauthBase+"/mcp" {
			t.Errorf("%s: status %d, body %v", path, w.Code, prm)
		}
	}
}

// --- registration ---

func TestOAuthRegister(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	out := e.register(t, `{"client_name":"Claude","redirect_uris":["`+oauthRedirect+`"],"token_endpoint_auth_method":"client_secret_basic"}`)
	id, _ := out["client_id"].(string)
	if id == "" {
		t.Fatalf("no client_id in %v", out)
	}
	// Replaced, never honoured: there are no secrets to issue.
	if out["token_endpoint_auth_method"] != "none" {
		t.Errorf("auth method = %v, want none", out["token_endpoint_auth_method"])
	}
	if _, ok := out["client_secret"]; ok {
		t.Error("a client secret was issued")
	}
	c, err := e.s.GetOAuthClient(id)
	if err != nil || c.Name != "Claude" {
		t.Errorf("stored client = %+v, %v", c, err)
	}
}

func TestOAuthRegisterRefusesABadRedirect(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	w := e.do(t, httptest.NewRequest("POST", oauth.PathRegister, strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != oauth.CodeInvalidRedirectURI {
		t.Errorf("error = %q", body["error"])
	}
}

func TestOAuthPreflight(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	for _, path := range []string{oauth.PathRegister, oauth.PathToken, oauth.PathServerMetadata} {
		req := httptest.NewRequest("OPTIONS", path, nil)
		req.Header.Set("Origin", "https://inspector.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		w := e.do(t, req)
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s preflight: status %d, headers %v", path, w.Code, w.Header())
		}
	}
}

// --- authorize ---

func TestAuthorizeSendsASignedOutOperatorThroughLogin(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	target := oauth.PathAuthorize + "?" + authorizeQuery(e.client(t)).Encode()
	w := e.do(t, httptest.NewRequest("GET", target, nil))
	loc := redirectTo(t, w)
	if loc.Path != "/admin/login" || loc.Query().Get("next") != target {
		t.Errorf("Location = %s, want login with next=%s", loc, target)
	}
}

// An authorization request whose client or redirect URI cannot be trusted gets
// a page and never a redirect (RFC 6749 §4.1.2.1): redirecting to an
// unverified URI is an open redirect that also carries the error to whoever
// owns it.
func TestAuthorizeNeverRedirectsForAnUntrustedClientOrURI(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	cases := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"unknown client", func(q url.Values) { q.Set("client_id", "nope") }},
		{"unregistered redirect", func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") }},
		{"redirect prefix", func(q url.Values) { q.Set("redirect_uri", oauthRedirect+"/x") }},
		{"no redirect", func(q url.Values) { q.Del("redirect_uri") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := authorizeQuery(clientID)
			tc.mutate(q)
			req := httptest.NewRequest("GET", oauth.PathAuthorize+"?"+q.Encode(), nil)
			req.AddCookie(c)
			w := e.do(t, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if loc := w.Header().Get("Location"); loc != "" {
				t.Errorf("redirected to %q", loc)
			}
		})
	}
}

func TestAuthorizeReturnsProtocolErrorsToTheClient(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	q := authorizeQuery(clientID)
	q.Set("code_challenge_method", "plain")
	req := httptest.NewRequest("GET", oauth.PathAuthorize+"?"+q.Encode(), nil)
	req.AddCookie(c)
	loc := redirectTo(t, e.do(t, req))

	if loc.Scheme+"://"+loc.Host+loc.Path != oauthRedirect {
		t.Errorf("redirected to %s", loc)
	}
	got := loc.Query()
	if got.Get("error") != oauth.CodeInvalidRequest || got.Get("state") != "st&ate" || got.Get("iss") != oauthBase {
		t.Errorf("query = %v", got)
	}
}

func TestConsentPageShowsWhoAndWhere(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)

	req := httptest.NewRequest("GET", oauth.PathAuthorize+"?"+authorizeQuery(clientID).Encode(), nil)
	req.AddCookie(e.cookie(t, "admin"))
	w := e.do(t, req)
	body := w.Body.String()

	for _, want := range []string{"<strong>Claude</strong>", "<strong>client.example.com</strong>", "as <strong>admin</strong>"} {
		if !strings.Contains(body, want) {
			t.Errorf("consent page does not show %q", want)
		}
	}
}

// The consent page starts at read, whatever the client asked for — the rule
// the token form follows. Clients routinely ask for every scope the server
// advertises, and pre-ticking the request made granting delete the path of
// least resistance. What was asked for is shown, so the operator ticks the
// rest deliberately.
func TestConsentStartsAtReadWhateverTheClientAsks(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)

	for _, asked := range []string{"read write delete", "write", ""} {
		q := authorizeQuery(clientID)
		q.Set("scope", asked)
		req := httptest.NewRequest("GET", oauth.PathAuthorize+"?"+q.Encode(), nil)
		req.AddCookie(e.cookie(t, "admin"))
		body := e.do(t, req).Body.String()

		for scope, want := range map[string]bool{"read": true, "write": false, "delete": false} {
			ticked := regexp.MustCompile(`name="scopes" value="` + scope + `"[^>]*checked`).MatchString(body)
			if ticked != want {
				t.Errorf("asked %q: scope %s ticked = %v, want %v", asked, scope, ticked, want)
			}
		}
		if asked == "read write delete" && !strings.Contains(body, "asked for read, write, delete") {
			t.Errorf("asked %q: the page does not say what the client asked for", asked)
		}
	}
}

func TestConsentRefusesATamperedForm(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	cases := []struct {
		name   string
		mutate func(url.Values)
	}{
		// Both registered, so only the signature can tell them apart.
		{"another registered redirect", func(f url.Values) { f.Set("redirect_uri", "https://client.example.com/other") }},
		{"swapped challenge", func(f url.Values) { f.Set("code_challenge", strings.Repeat("A", 43)) }},
		{"changed state", func(f url.Values) { f.Set("state", "other") }},
		{"no signature", func(f url.Values) { f.Del("consent") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			form := e.consentForm(t, c, authorizeQuery(clientID))
			form.Set("action", "approve")
			form["scopes"] = []string{"read"}
			tc.mutate(form)
			w := e.postConsent(t, c, form)
			if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
				t.Errorf("status %d, Location %q; want 400 and no redirect", w.Code, w.Header().Get("Location"))
			}
			if !strings.Contains(w.Body.String(), "expired or was not issued to you") {
				t.Error("the page does not say why")
			}
			if n := e.codes(t); n != 0 {
				t.Errorf("a tampered form issued %d codes", n)
			}
		})
	}
}

// A consent page rendered for one operator cannot be submitted by another: the
// signature binds the user, so a page left open on a shared machine, or one
// fetched by somebody else, approves nothing.
func TestConsentIsBoundToTheOperatorItWasShownTo(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)

	form := e.consentForm(t, e.cookie(t, "admin"), authorizeQuery(clientID))
	form.Set("action", "approve")
	form["scopes"] = []string{"read"}
	w := e.postConsent(t, e.cookie(t, "someone"), form)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Errorf("status %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	if n := e.codes(t); n != 0 {
		t.Errorf("another operator's post issued %d codes", n)
	}
}

func TestConsentDeny(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	form := e.consentForm(t, c, authorizeQuery(clientID))
	form.Set("action", "deny")
	loc := redirectTo(t, e.postConsent(t, c, form))
	q := loc.Query()
	if q.Get("error") != oauth.CodeAccessDenied || q.Get("state") != "st&ate" || q.Get("iss") != oauthBase || q.Get("code") != "" {
		t.Errorf("deny redirect = %s", loc)
	}
}

func TestConsentWithNothingChosenAsksAgain(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	form := e.consentForm(t, c, authorizeQuery(clientID))
	form.Set("action", "approve")
	w := e.postConsent(t, c, form)
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
		t.Fatalf("status %d, Location %q; want the page again", w.Code, w.Header().Get("Location"))
	}
	if !strings.Contains(w.Body.String(), `class="notice"`) {
		t.Error("no message saying what is missing")
	}

	// The page that comes back is still a working, signed approval: tick
	// something and it goes through, state and all.
	again := url.Values{}
	for _, m := range hiddenField.FindAllStringSubmatch(w.Body.String(), -1) {
		again.Set(m[1], html.UnescapeString(m[2]))
	}
	again.Set("action", "approve")
	again["scopes"] = []string{"read"}
	loc := redirectTo(t, e.postConsent(t, c, again))
	if loc.Query().Get("code") == "" || loc.Query().Get("state") != "st&ate" {
		t.Errorf("approving the re-rendered page redirected to %s", loc)
	}
}

func TestConsentApproveIssuesACode(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	form := e.consentForm(t, c, authorizeQuery(clientID))
	form.Set("action", "approve")
	form["scopes"] = []string{"read"}
	loc := redirectTo(t, e.postConsent(t, c, form))
	q := loc.Query()
	if q.Get("code") == "" || q.Get("state") != "st&ate" || q.Get("iss") != oauthBase {
		t.Errorf("approve redirect = %s", loc)
	}
}

// --- token ---

func TestTokenExchange(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	code := e.approve(t, clientID)

	w, body := e.token(t, codeExchange(clientID, code))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %v", w.Code, body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("token response may be cached")
	}
	if body["token_type"] != "Bearer" || body["scope"] != "read write" || body["expires_in"] != float64(3600) {
		t.Errorf("body = %v", body)
	}
	access, _ := body["access_token"].(string)
	tok, err := e.s.GetAPIToken(access)
	if err != nil {
		t.Fatalf("issued access token does not verify: %v", err)
	}
	if tok.Name != "Claude" {
		t.Errorf("token name = %q", tok.Name)
	}
	// What is stored, not just what the response says: a response that agreed
	// with the request while the token held more would pass on the body alone.
	if !slices.Equal(tok.Scopes, []string{"read", "write"}) || !tok.Scope().All() {
		t.Errorf("stored token scopes %v, all forms %v", tok.Scopes, tok.Scope().All())
	}
	if r, _ := body["refresh_token"].(string); !strings.HasPrefix(r, store.RefreshTokenPrefix) {
		t.Errorf("refresh_token = %v", body["refresh_token"])
	}
}

func TestTokenExchangeRefusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(f url.Values, otherClient string)
		code   string
	}{
		{"wrong verifier", func(f url.Values, _ string) { f.Set("code_verifier", strings.Repeat("a", 43)) }, oauth.CodeInvalidGrant},
		{"no verifier", func(f url.Values, _ string) { f.Del("code_verifier") }, oauth.CodeInvalidGrant},
		{"another redirect", func(f url.Values, _ string) { f.Set("redirect_uri", "https://client.example.com/other") }, oauth.CodeInvalidGrant},
		{"another client", func(f url.Values, other string) { f.Set("client_id", other) }, oauth.CodeInvalidGrant},
		{"another resource", func(f url.Values, _ string) { f.Set("resource", "https://other.example/mcp") }, oauth.CodeInvalidGrant},
		{"unknown code", func(f url.Values, _ string) { f.Set("code", "nope") }, oauth.CodeInvalidGrant},
		{"unsupported grant", func(f url.Values, _ string) { f.Set("grant_type", "password") }, oauth.CodeUnsupportedGrantType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setupOAuth(t)
			clientID := e.client(t)
			other := e.client(t)
			code := e.approve(t, clientID)

			form := codeExchange(clientID, code)
			tc.mutate(form, other)
			w, body := e.token(t, form)
			if w.Code != http.StatusBadRequest || body["error"] != tc.code {
				t.Errorf("status %d, body %v; want 400 %s", w.Code, body, tc.code)
			}
			if _, ok := body["access_token"]; ok {
				t.Error("an access token came back with an error")
			}
		})
	}
}

// A failed exchange spends the code: an attacker holding an intercepted code
// gets one guess at the verifier, not as many as they like.
func TestAFailedExchangeBurnsTheCode(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	code := e.approve(t, clientID)

	bad := codeExchange(clientID, code)
	bad.Set("code_verifier", strings.Repeat("a", 43))
	e.token(t, bad)

	w, body := e.token(t, codeExchange(clientID, code))
	if w.Code != http.StatusBadRequest || body["error"] != oauth.CodeInvalidGrant {
		t.Errorf("second attempt: status %d, body %v", w.Code, body)
	}
}

func TestTokenCodeReplayRevokesTheIssuedToken(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	code := e.approve(t, clientID)

	_, first := e.token(t, codeExchange(clientID, code))
	w, _ := e.token(t, codeExchange(clientID, code))
	if w.Code != http.StatusBadRequest {
		t.Errorf("replay status = %d", w.Code)
	}
	if _, err := e.s.GetAPIToken(first["access_token"].(string)); err == nil {
		t.Error("the token issued before the replay still works")
	}
}

func TestTokenAcceptsClientIDFromBasicAuth(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	code := e.approve(t, clientID)

	// Some clients send the id the client_secret_basic way even when told they
	// are public. An empty secret is still a public client.
	form := codeExchange(clientID, code)
	form.Del("client_id")
	req := httptest.NewRequest("POST", oauth.PathToken, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, "")
	if w := e.do(t, req); w.Code != http.StatusOK {
		t.Errorf("status = %d, body %s", w.Code, w.Body)
	}
}

func TestTokenRefresh(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	_, first := e.token(t, codeExchange(clientID, e.approve(t, clientID)))

	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first["refresh_token"].(string)}, "client_id": {clientID}}
	w, second := e.token(t, refresh)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body %v", w.Code, second)
	}
	if second["refresh_token"] == first["refresh_token"] || second["access_token"] == first["access_token"] {
		t.Error("refresh did not rotate")
	}

	// The spent one again: reuse revokes everything.
	w, body := e.token(t, refresh)
	if w.Code != http.StatusBadRequest || body["error"] != oauth.CodeInvalidGrant {
		t.Errorf("reuse: status %d, body %v", w.Code, body)
	}
	if _, err := e.s.GetAPIToken(second["access_token"].(string)); err == nil {
		t.Error("reuse left the current access token live")
	}
}

func TestTokenRefreshForAClientThatDidNotRegisterForIt(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.register(t, `{"redirect_uris":["`+oauthRedirect+`"],"grant_types":["authorization_code"]}`)["client_id"].(string)
	_, first := e.token(t, codeExchange(clientID, e.approve(t, clientID)))

	if _, ok := first["refresh_token"]; ok {
		t.Error("a refresh token was issued to a client that did not register for one")
	}
}

// Registration is open to anyone, and the per-IP rate limit trusts
// X-Forwarded-For, so it bounds nothing on its own. At the cap, the oldest
// unapproved clients make room: refusing instead would let a few hundred
// requests keep every legitimate client out. An approved client is never
// evicted.
func TestRegistrationAtTheCapEvictsTheOldestUnapproved(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	approved := e.client(t)
	e.token(t, codeExchange(approved, e.approve(t, approved)))
	for i := 0; i < MaxPendingOAuthClients; i++ {
		if _, err := e.s.CreateOAuthClient("filler", []string{oauthRedirect}, []string{"authorization_code"}); err != nil {
			t.Fatal(err)
		}
	}

	w := e.do(t, httptest.NewRequest("POST", oauth.PathRegister, strings.NewReader(`{"redirect_uris":["`+oauthRedirect+`"]}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body)
	}
	var pending int
	if err := e.s.DB().QueryRow("SELECT COUNT(*) FROM oauth_clients WHERE id NOT IN (SELECT client_id FROM oauth_grants)").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending > MaxPendingOAuthClients {
		t.Errorf("%d unapproved clients, want at most %d", pending, MaxPendingOAuthClients)
	}
	if _, err := e.s.GetOAuthClient(approved); err != nil {
		t.Errorf("an approved client was evicted: %v", err)
	}
}

func TestRegistrationRefusesMalformedRequests(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	for body, code := range map[string]string{
		`not json`:             oauth.CodeInvalidClientMetadata,
		`{"redirect_uris":[]}`: oauth.CodeInvalidRedirectURI,
		`{"client_name":"x"}`:  oauth.CodeInvalidRedirectURI,
		`{"redirect_uris":["https://a.example/cb"],"response_types":["token"]}`: oauth.CodeInvalidClientMetadata,
	} {
		w := e.do(t, httptest.NewRequest("POST", oauth.PathRegister, strings.NewReader(body)))
		var got map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != http.StatusBadRequest || got["error"] != code {
			t.Errorf("%s: status %d, error %q; want 400 %s", body, w.Code, got["error"], code)
		}
	}
}

// RFC 6749 §5.2: a client that authenticated with the Authorization header and
// failed gets a 401 with a challenge naming the scheme it used.
func TestTokenUnknownClientChallengesBasicAuth(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	req := httptest.NewRequest("POST", oauth.PathToken, strings.NewReader("grant_type=authorization_code&code=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("no-such-client", "")
	w := e.do(t, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge", got)
	}
}

func TestTokenWithNoClientAtAll(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)

	w, body := e.token(t, url.Values{"grant_type": {"authorization_code"}, "code": {"x"}})
	if w.Code != http.StatusUnauthorized || body["error"] != oauth.CodeInvalidClient {
		t.Errorf("status %d, body %v; want 401 invalid_client", w.Code, body)
	}
	// No Authorization header was sent, so there is no scheme to challenge.
	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q on a request that used no header", got)
	}
}

// A consent page left open past its lifetime approves nothing, through the
// real handler and clock, not just through the signer.
func TestAnExpiredConsentPageApprovesNothing(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	form := e.consentForm(t, c, authorizeQuery(clientID))
	*e.now = e.now.Add(oauth.ConsentTTL + time.Second)
	form.Set("action", "approve")
	form["scopes"] = []string{"read"}

	w := e.postConsent(t, c, form)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Errorf("status %d, Location %q; want 400 and no redirect", w.Code, w.Header().Get("Location"))
	}
	if n := e.codes(t); n != 0 {
		t.Errorf("an expired page issued %d codes", n)
	}
}

// The client is checked again on submit: it may have been evicted or pruned
// while the page sat open.
func TestConsentForAClientRemovedSinceThePageRendered(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	form := e.consentForm(t, c, authorizeQuery(clientID))
	if _, err := e.s.DB().Exec("DELETE FROM oauth_clients WHERE id = ?", clientID); err != nil {
		t.Fatal(err)
	}
	form.Set("action", "approve")
	form["scopes"] = []string{"read"}

	w := e.postConsent(t, c, form)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Errorf("status %d, Location %q; want 400 and no redirect", w.Code, w.Header().Get("Location"))
	}
	if n := e.codes(t); n != 0 {
		t.Errorf("a removed client was issued %d codes", n)
	}
}

// Consent can bound a client to particular forms, by the same rules as the
// token form — and the token issued holds exactly that bound.
func TestConsentBindsTheClientToTheFormsChosen(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		reach string
		forms []string
		want  []string
	}{
		{"listed forms", "listed", []string{"f1"}, []string{"f1"}},
		// A ticked form binds whatever the radio says, as on the token form.
		{"ticked form with the radio left at all", "all", []string{"f2"}, []string{"f2"}},
		{"all forms", "all", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setupOAuth(t)
			for _, f := range []store.Form{{ID: "f1", Name: "Contact", EmailTo: "a@b.com"}, {ID: "f2", Name: "Careers", EmailTo: "a@b.com"}} {
				if err := e.s.CreateForm(f); err != nil {
					t.Fatal(err)
				}
			}
			clientID := e.client(t)
			c := e.cookie(t, "admin")

			form := e.consentForm(t, c, authorizeQuery(clientID))
			form.Set("action", "approve")
			form["scopes"] = []string{"read"}
			form.Set("reach", tc.reach)
			form["form_ids"] = tc.forms
			code := redirectTo(t, e.postConsent(t, c, form)).Query().Get("code")

			_, body := e.token(t, codeExchange(clientID, code))
			tok, err := e.s.GetAPIToken(body["access_token"].(string))
			if err != nil {
				t.Fatalf("issued token does not verify: %v", err)
			}
			if !slices.Equal(tok.FormIDs, tc.want) {
				t.Errorf("token reaches %v, want %v", tok.FormIDs, tc.want)
			}
		})
	}
}

func TestConsentRefusesAnUnknownForm(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	c := e.cookie(t, "admin")

	form := e.consentForm(t, c, authorizeQuery(clientID))
	form.Set("action", "approve")
	form["scopes"] = []string{"read"}
	form.Set("reach", "listed")
	form["form_ids"] = []string{"no-such-form"}

	w := e.postConsent(t, c, form)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "form with id no-such-form") {
		t.Errorf("status %d; want the page again naming the form", w.Code)
	}
	if n := e.codes(t); n != 0 {
		t.Errorf("an unknown form still issued %d codes", n)
	}
}

func TestConsentSaysWhenFormsCannotBeLoaded(t *testing.T) {
	t.Parallel()
	e := setupOAuthWith(t, func(s *store.Store) OAuthStore { return failingStore{Store: s, listForms: errDatabase} })
	clientID := e.client(t)

	req := httptest.NewRequest("GET", oauth.PathAuthorize+"?"+authorizeQuery(clientID).Encode(), nil)
	req.AddCookie(e.cookie(t, "admin"))
	body := e.do(t, req).Body.String()
	if !strings.Contains(body, "could not be loaded") {
		t.Error("the page does not say the forms could not be loaded")
	}
	// The claim it must not make when it simply does not know.
	if strings.Contains(body, "No forms yet") {
		t.Error("the page claims there are no forms")
	}
}

// A database failure is not a bad credential. Answering invalid_grant or
// invalid_client would make the client throw away a code or registration that
// still works; server_error makes it retry.
func TestTransientFailuresAreServerErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		fail  failingStore
		grant string
	}{
		{"client lookup", failingStore{getClient: errDatabase}, "authorization_code"},
		{"code exchange", failingStore{exchange: errDatabase}, "authorization_code"},
		{"refresh", failingStore{refresh: errDatabase}, "refresh_token"},
		// A detected replay whose revocation failed: tokens may be live, and
		// the answer must not look like routine refusal.
		{"failed revocation after replay", failingStore{exchange: errors.Join(store.ErrCodeReused, store.ErrRevokeFailed, errDatabase)}, "authorization_code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setupOAuthWith(t, func(s *store.Store) OAuthStore { f := tc.fail; f.Store = s; return f })
			c, err := e.s.CreateOAuthClient("x", []string{oauthRedirect}, []string{"authorization_code", "refresh_token"})
			if err != nil {
				t.Fatal(err)
			}
			w, body := e.token(t, url.Values{"grant_type": {tc.grant}, "client_id": {c.ID}, "code": {"c"}, "refresh_token": {"r"}})
			if w.Code != http.StatusInternalServerError || body["error"] != oauth.CodeServerError {
				t.Errorf("status %d, body %v; want 500 server_error", w.Code, body)
			}
		})
	}
}

func TestTokenRefreshRefusals(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	clientID := e.client(t)
	other := e.client(t)
	_, first := e.token(t, codeExchange(clientID, e.approve(t, clientID)))
	refresh := first["refresh_token"].(string)

	cases := map[string]url.Values{
		"another client's token": {"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {other}},
		"no token":               {"grant_type": {"refresh_token"}, "client_id": {clientID}},
	}
	for name, form := range cases {
		w, body := e.token(t, form)
		if w.Code != http.StatusBadRequest || body["error"] != oauth.CodeInvalidGrant {
			t.Errorf("%s: status %d, body %v; want 400 invalid_grant", name, w.Code, body)
		}
	}
	// And the refused attempt from the other client did not spend it.
	w, _ := e.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if w.Code != http.StatusOK {
		t.Errorf("the owner's refresh after a refused one: %d", w.Code)
	}
}

// The unauthorized_client branch itself: a client that registered without
// refresh presents a refresh token anyway.
func TestRefreshFromAClientNotRegisteredForIt(t *testing.T) {
	t.Parallel()
	e := setupOAuth(t)
	c, err := e.s.CreateOAuthClient("x", []string{oauthRedirect}, []string{"authorization_code"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := e.s.GetUserByUsername("admin")
	raw, err := e.s.CreateAuthCode(store.AuthCode{ClientID: c.ID, UserID: u.ID, RedirectURI: oauthRedirect, CodeChallenge: pkceChallenge, Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := e.s.ExchangeAuthCode(raw, func(store.AuthCode) bool { return true })
	if err != nil {
		t.Fatal(err)
	}

	w, body := e.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {pair.Refresh}, "client_id": {c.ID}})
	if w.Code != http.StatusBadRequest || body["error"] != oauth.CodeUnauthorizedClient {
		t.Errorf("status %d, body %v; want 400 unauthorized_client", w.Code, body)
	}
}
