package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

const testSecret = "test-secret-key-32-chars-long!!"

type mockUserStore struct {
	user store.User
	err  error
}

func (m *mockUserStore) GetUserByID(id string) (store.User, error) {
	if m.err != nil {
		return store.User{}, m.err
	}
	if id != m.user.ID {
		return store.User{}, fmt.Errorf("user %q not found", id)
	}
	return m.user, nil
}

func TestCreateSessionCookieAttributes(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	if !c.HttpOnly {
		t.Error("HttpOnly = false, want true")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if !c.Secure {
		t.Error("Secure = false, want true for https baseURL")
	}
	if c.Name != CookieName {
		t.Errorf("Name = %q, want %q", c.Name, CookieName)
	}
}

func TestCreateSessionCookieSecureFalseForHTTP(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "http://localhost:8080")
	if c.Secure {
		t.Error("Secure = true, want false for http baseURL")
	}
}

func TestCreateSessionCookiePathAndMaxAge(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	expectedMaxAge := 30 * 24 * 60 * 60
	if c.MaxAge != expectedMaxAge {
		t.Errorf("MaxAge = %d, want %d", c.MaxAge, expectedMaxAge)
	}
}

func TestCreateSessionCookieContainsUserID(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	if c.Value == "" {
		t.Fatal("cookie value is empty")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	userID, ok := ValidateSession(req, testSecret)
	if !ok {
		t.Fatal("ValidateSession returned ok=false for fresh cookie")
	}
	if userID != "user-123" {
		t.Errorf("userID = %q, want %q", userID, "user-123")
	}
}

func TestValidateSessionTamperedCookie(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	c.Value = c.Value + "tampered"
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	_, ok := ValidateSession(req, testSecret)
	if ok {
		t.Fatal("ValidateSession returned ok=true for tampered cookie")
	}
}

func TestValidateSessionExpiredCookie(t *testing.T) {
	t.Parallel()
	val := createSessionValue("user-123", testSecret, time.Now().Add(-31*24*time.Hour))
	c := &http.Cookie{Name: CookieName, Value: val}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	_, ok := ValidateSession(req, testSecret)
	if ok {
		t.Fatal("ValidateSession returned ok=true for expired cookie")
	}
}

func TestValidateSessionNotExpiredAt29Days(t *testing.T) {
	t.Parallel()
	val := createSessionValue("user-123", testSecret, time.Now().Add(-29*24*time.Hour))
	c := &http.Cookie{Name: CookieName, Value: val}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	userID, ok := ValidateSession(req, testSecret)
	if !ok {
		t.Fatal("ValidateSession returned ok=false for 29-day-old cookie")
	}
	if userID != "user-123" {
		t.Errorf("userID = %q, want user-123", userID)
	}
}

func TestValidateSessionMissingCookie(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	_, ok := ValidateSession(req, testSecret)
	if ok {
		t.Fatal("ValidateSession returned ok=true for missing cookie")
	}
}

func TestValidateSessionWrongKey(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	_, ok := ValidateSession(req, "wrong-secret-key-32-chars-long!!")
	if ok {
		t.Fatal("ValidateSession returned ok=true with wrong secret key")
	}
}

func TestClearSessionCookie(t *testing.T) {
	t.Parallel()
	c := ClearSessionCookie()
	if c.MaxAge != -1 {
		t.Errorf("MaxAge = %d, want -1", c.MaxAge)
	}
	if c.Name != CookieName {
		t.Errorf("Name = %q, want %q", c.Name, CookieName)
	}
}

func TestRequireAuthAllowsValidSession(t *testing.T) {
	t.Parallel()
	mock := &mockUserStore{
		user: store.User{ID: "user-123", Username: "admin"},
	}
	handler := RequireAuth(mock, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireAuthRedirectsInvalidSession(t *testing.T) {
	t.Parallel()
	mock := &mockUserStore{}
	handler := RequireAuth(mock, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}
}

func TestRequireAuthLoadsUserIntoContext(t *testing.T) {
	t.Parallel()
	mock := &mockUserStore{
		user: store.User{ID: "user-123", Username: "admin", IsDefaultPassword: true},
	}
	var gotUser store.User
	handler := RequireAuth(mock, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			t.Fatal("UserFromContext returned ok=false")
		}
		gotUser = u
		w.WriteHeader(http.StatusOK)
	}))
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if gotUser.Username != "admin" {
		t.Errorf("Username = %q, want admin", gotUser.Username)
	}
	if !gotUser.IsDefaultPassword {
		t.Error("IsDefaultPassword = false, want true")
	}
}

func TestRequireAuthRedirectsWhenUserNotFound(t *testing.T) {
	t.Parallel()
	mock := &mockUserStore{
		err: fmt.Errorf("user not found"),
	}
	handler := RequireAuth(mock, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	c := CreateSessionCookie("deleted-user", testSecret, "https://example.com")
	req := httptest.NewRequest("GET", "/admin/forms", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
}
