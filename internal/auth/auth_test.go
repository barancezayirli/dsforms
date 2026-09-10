package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/store"
)

func TestCreateSessionCookieAttributes(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("abc123token", "https://example.com")
	if !c.HttpOnly {
		t.Error("HttpOnly = false")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Error("SameSite != Lax")
	}
	if !c.Secure {
		t.Error("Secure = false for https")
	}
	if c.Path != "/" {
		t.Errorf("Path = %q", c.Path)
	}
	if c.MaxAge != 30*24*60*60 {
		t.Errorf("MaxAge = %d", c.MaxAge)
	}
	if c.Value != "abc123token" {
		t.Errorf("Value = %q, want abc123token", c.Value)
	}
	if c.Name != CookieName {
		t.Errorf("Name = %q", c.Name)
	}
}

func TestCreateSessionCookieSecureFalseHTTP(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("token", "http://localhost")
	if c.Secure {
		t.Error("Secure = true for http")
	}
}

func TestGetSessionToken(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "mytoken"})
	token, ok := GetSessionToken(req)
	if !ok {
		t.Fatal("ok = false")
	}
	if token != "mytoken" {
		t.Errorf("token = %q", token)
	}
}

func TestGetSessionTokenMissing(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	_, ok := GetSessionToken(req)
	if ok {
		t.Fatal("ok = true for missing cookie")
	}
}

func TestGetSessionTokenEmpty(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: ""})
	_, ok := GetSessionToken(req)
	if ok {
		t.Fatal("ok = true for empty cookie value")
	}
}

func TestClearSessionCookie(t *testing.T) {
	t.Parallel()
	c := ClearSessionCookie()
	if c.MaxAge != -1 {
		t.Errorf("MaxAge = %d", c.MaxAge)
	}
	if c.Name != CookieName {
		t.Errorf("Name = %q", c.Name)
	}
}

func TestRequireAuthValidSession(t *testing.T) {
	t.Parallel()
	s, _ := store.New(":memory:")
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)

	handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			t.Fatal("no user in context")
		}
		if u.Username != "admin" {
			t.Errorf("Username = %q", u.Username)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireAuthInvalidToken(t *testing.T) {
	t.Parallel()
	s, _ := store.New(":memory:")
	handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "invalid-token"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	// Should clear the stale cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName && c.MaxAge == -1 {
			return
		}
	}
	t.Error("stale cookie not cleared")
}

func TestRequireAuthNoCookie(t *testing.T) {
	t.Parallel()
	s, _ := store.New(":memory:")
	handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
}

func TestRequireAuthExpiredSession(t *testing.T) {
	t.Parallel()
	s, _ := store.New(":memory:")
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, -1*time.Hour) // already expired
	handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
}

func TestRequireAuthDeletedSession(t *testing.T) {
	t.Parallel()
	s, _ := store.New(":memory:")
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	s.DeleteSession(token) // simulate logout
	handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 after session deleted", w.Code)
	}
}

// TestRequireAuthAnswers401ToFragmentRequests covers a UI lock-up rather than an
// auth hole. fetch follows redirects transparently and /admin/login answers 200
// with a full HTML page, so redirecting a fragment request made r.ok true and
// the login page was injected into the reader drawer — where Escape, the
// backdrop and the close button all select elements that page does not contain,
// leaving the operator with a scroll-locked overlay and only a reload to escape.
func TestRequireAuthAnswers401ToFragmentRequests(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name     string
		fragment bool
		cookie   *http.Cookie
		want     int
	}{
		{name: "no session, ordinary request redirects", want: http.StatusFound},
		{name: "no session, fragment request is unauthorized", fragment: true, want: http.StatusUnauthorized},
		{
			name:     "invalid session, fragment request is unauthorized",
			fragment: true,
			cookie:   CreateSessionCookie("not-a-real-token", "https://example.com"),
			want:     http.StatusUnauthorized,
		},
		{
			name:   "invalid session, ordinary request redirects",
			cookie: CreateSessionCookie("not-a-real-token", "https://example.com"),
			want:   http.StatusFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", "/admin/forms/f1/submissions/s1", nil)
			if tt.fragment {
				req.Header.Set("X-Fragment", "1")
			}
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
			// A 401 must not carry a redirect the client could follow.
			if tt.want == http.StatusUnauthorized && w.Header().Get("Location") != "" {
				t.Errorf("401 response also set Location: %q", w.Header().Get("Location"))
			}
		})
	}
}
