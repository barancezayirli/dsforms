package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/youruser/dsforms/internal/store"
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
