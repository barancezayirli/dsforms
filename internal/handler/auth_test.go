package handler

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/ratelimit"
	"github.com/youruser/dsforms/internal/store"
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

	tmpl := template.Must(template.New("login.html").Parse(
		`<!DOCTYPE html><html><body>` +
			`{{if .LoginError}}<p>Invalid username or password</p>{{end}}` +
			`<form method="POST" action="/admin/login">` +
			`<input name="username"><input name="password" type="password">` +
			`<button type="submit">Log in</button></form></body></html>`))

	ah := &AuthHandler{
		Store:      s,
		SecretKey:  testSecretKey,
		BaseURL:    "https://example.com",
		LoginGuard: guard,
		Templates:  tmpl,
	}

	r := chi.NewRouter()
	r.Get("/admin/login", ah.LoginPage)
	r.Post("/admin/login", ah.LoginSubmit)
	r.Post("/admin/logout", ah.Logout)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s, testSecretKey))
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
	cookie := auth.CreateSessionCookie(admin.ID, testSecretKey, "https://example.com")
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
	cookie := auth.CreateSessionCookie(admin.ID, testSecretKey, "https://example.com")
	cookie.Value += "tampered"
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
}
