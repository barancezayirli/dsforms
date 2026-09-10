package handler

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

const testSecretKey = "test-secret-key-32-chars-long!!"

func setupAuth(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	now := time.Now()
	guard := ratelimit.NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })

	loginTmpl := template.Must(template.New("login.html").Parse(
		`<!DOCTYPE html><html><body>` +
			`{{if .LoginError}}<p>Invalid username or password</p>{{end}}` +
			`<form method="POST" action="/admin/login">` +
			`<input name="username"><input name="password" type="password">` +
			`<button type="submit">Log in</button></form></body></html>`))

	templates := map[string]*template.Template{
		"login.html": loginTmpl,
	}

	ah := &AuthHandler{
		Store: s,
		Base: Base{
			Nav:       s,
			SecretKey: testSecretKey,
			BaseURL:   "https://example.com",
			Templates: templates,
		},
		LoginGuard: guard,
	}

	r := chi.NewRouter()
	r.Get("/admin/login", ah.LoginPage)
	r.Post("/admin/login", ah.LoginSubmit)
	r.Post("/admin/logout", ah.Logout)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/forms", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("dashboard"))
		})
	})

	return s, r
}

func TestLoginPageReturns200(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	req := httptest.NewRequest("GET", "/admin/login", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestLoginPageErrorParam(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	req := httptest.NewRequest("GET", "/admin/login?error=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid username or password") {
		t.Error("error message not rendered")
	}
}

func TestLoginSubmitValidCredentials(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	form := url.Values{"username": {"admin"}, "password": {"admin"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/forms" {
		t.Errorf("Location = %q, want /admin/forms", loc)
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			found = true
		}
	}
	if !found {
		t.Error("session cookie not set")
	}
}

func TestLoginSubmitWrongPassword(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login?error=1" {
		t.Errorf("Location = %q, want /admin/login?error=1", loc)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			t.Error("session cookie set on failed login")
		}
	}
}

func TestLoginSubmitWrongUsername(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	form := url.Values{"username": {"nonexistent"}, "password": {"admin"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login?error=1" {
		t.Errorf("Location = %q, want /admin/login?error=1", loc)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			t.Error("session cookie set on failed login")
		}
	}
}

func TestLoginSubmitLockout(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	req := httptest.NewRequest("POST", "/admin/logout", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName && c.MaxAge == -1 {
			return
		}
	}
	t.Error("session cookie not cleared")
}

func TestAdminGuardNoCookie(t *testing.T) {
	t.Parallel()
	_, r := setupAuth(t)
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}
}

func TestAdminGuardValidCookie(t *testing.T) {
	t.Parallel()
	s, r := setupAuth(t)
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	cookie := auth.CreateSessionCookie(token, "https://example.com")
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAdminGuardTamperedCookie(t *testing.T) {
	t.Parallel()
	s, r := setupAuth(t)
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	cookie := auth.CreateSessionCookie(token, "https://example.com")
	cookie.Value += "tampered"
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
}

// failingSessionStore deletes nothing and says so.
type failingSessionStore struct {
	AuthStore
	err error
}

func (f failingSessionStore) DeleteSession(string) error { return f.err }

// TestLogoutSaysSoWhenTheSessionSurvives is the regression test for a silent
// security failure.
//
// Logout called DeleteSession and discarded the error. The cookie was cleared
// either way, so the operator saw a completely normal logout — while the session
// row survived and RequireAuth kept accepting that token for up to 30 days.
// Anyone holding it (a shared machine, a captured cookie, a browser backup)
// stayed authenticated, and nothing anywhere said otherwise.
//
// Clearing the cookie is still right on the failure path — it is strictly better
// than leaving it — but it must not be the whole response, because it is the part
// that makes the failure invisible.
func TestLogoutSaysSoWhenTheSessionSurvives(t *testing.T) {
	t.Parallel()
	s, _ := setupAuth(t)

	ah := &AuthHandler{
		Store: failingSessionStore{AuthStore: s, err: errors.New("database is unavailable")},
		Base:  Base{Nav: s, SecretKey: testSecretKey, BaseURL: "https://example.com"},
	}

	admin, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("admin user: %v", err)
	}
	token, err := s.CreateSession(admin.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest("POST", "/admin/logout", nil)
	req.AddCookie(auth.CreateSessionCookie(token, "https://example.com"))
	w := httptest.NewRecorder()
	ah.Logout(w, req)

	// The cookie must still be cleared: it costs nothing and it is the only part
	// of the logout that definitely worked.
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the session cookie was not cleared; a failed delete must not also " +
			"leave the browser holding the token")
	}

	// And it must not look like a clean logout.
	loc := w.Header().Get("Location")
	if loc == "/admin/login" {
		t.Fatal("a failed logout redirected exactly like a successful one.\n" +
			"The session is still valid on the server for up to 30 days and the " +
			"operator has no way to know, so they cannot change their password in " +
			"response.")
	}
	if !strings.Contains(loc, "logout=incomplete") {
		t.Errorf("Location = %q, want the incomplete-logout signal", loc)
	}
}

// TestLogoutSucceedsQuietly is the other half: the normal path must not start
// warning people for no reason, or the warning stops meaning anything.
func TestLogoutSucceedsQuietly(t *testing.T) {
	t.Parallel()
	s, _ := setupAuth(t)
	ah := &AuthHandler{
		Store: s,
		Base:  Base{Nav: s, SecretKey: testSecretKey, BaseURL: "https://example.com"},
	}

	admin, _ := s.GetUserByUsername("admin")
	token, err := s.CreateSession(admin.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest("POST", "/admin/logout", nil)
	req.AddCookie(auth.CreateSessionCookie(token, "https://example.com"))
	w := httptest.NewRecorder()
	ah.Logout(w, req)

	if loc := w.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("Location = %q, want a clean /admin/login", loc)
	}
	// And the session really is gone.
	if _, err := s.GetSession(token); err == nil {
		t.Error("the session still resolves to a user after a successful logout")
	}
}

// TestLoginPageRendersTheIncompleteLogoutWarning closes the seam, not just the
// handler's half of it.
//
// Logout signals a failed invalidation with ?logout=incomplete, and LoginPage
// turns that into LogoutIncomplete. None of that reaches the operator unless the
// real template renders the field — and a warning nobody sees is the same as no
// warning, which is the state this whole fix exists to leave behind.
//
// This repo has shipped that exact shape twice: a template posting a field name
// no handler read, and a Degraded flag the template could not reach. Both halves
// were individually correct; nothing owned the join.
func TestLoginPageRendersTheIncompleteLogoutWarning(t *testing.T) {
	t.Parallel()

	// Parsed with the sprite, the way main.go loads it: login.html includes
	// {{template "icons"}}, so on its own it cannot execute at all.
	tmpl, err := template.ParseFiles(
		filepath.Join(templateDir, "login.html"),
		filepath.Join(templateDir, "icons.html"),
	)
	if err != nil {
		t.Fatalf("parsing the real login.html: %v", err)
	}

	// Whitespace-normalised, because the copy wraps across lines in the template
	// and an assertion that breaks on re-indentation is a test that fails for the
	// wrong reason.
	render := func(data LoginData) string {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("executing login.html: %v", err)
		}
		return strings.Join(strings.Fields(buf.String()), " ")
	}

	warned := render(LoginData{LogoutIncomplete: true})
	if !strings.Contains(warned, "could not be ended on the server") {
		t.Error("login.html renders nothing for LogoutIncomplete.\n" +
			"The handler sets it, the redirect carries it, and the operator still " +
			"never learns their session is live.")
	}
	// It must say what to do, not merely that something failed.
	if !strings.Contains(warned, "change your password") {
		t.Error("the warning does not tell the operator to change their password, " +
			"which is the only action that revokes the session that is still valid")
	}

	if quiet := render(LoginData{}); strings.Contains(quiet, "could not be ended on the server") {
		t.Error("the warning renders on an ordinary login, so it will be ignored " +
			"by the time it matters")
	}
}
