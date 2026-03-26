# Session 4 — Auth (Session Cookies + Middleware + Flash) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Session cookie creation/validation with HMAC signing, RequireAuth middleware that loads user into context, and one-time flash messages via signed cookies.

**Architecture:** Two independent packages: `internal/auth` (session cookies + middleware) and `internal/flash` (signed cookie flash messages). Auth uses HMAC-SHA256 to sign cookies with the app's SECRET_KEY. The cookie encodes `userID:timestamp:signature`. RequireAuth middleware validates the cookie, loads the User from the store, and puts it on the request context. Flash uses the same HMAC signing pattern for one-time messages.

**Tech Stack:** Go stdlib (`crypto/hmac`, `crypto/sha256`, `encoding/base64`, `net/http`). No new dependencies.

---

## Deferred items to address

- **Session 2:** "passwordHash field on User is unexported — auth package will need an accessor". Fix: export the field as `PasswordHash string` on the User struct in store.go.
- **Session 1:** "BASE_URL empty string has no startup warning". Fix: the Secure flag on cookies is set conditionally based on baseURL starting with "https" — no panic needed, just correct behavior.

## Deviations from spec

- `CreateSessionCookie` takes 3 params `(userID, secretKey, baseURL string)` instead of 2. The baseURL is needed to set the Secure flag conditionally (DSFORMS_PLAN.md §5 says "Secure: true if BASE_URL starts with https").
- Cookie must set `Path: "/"` (not in spec but required for cookie to be sent on all paths).
- Cookie must set `MaxAge: 30 * 24 * 60 * 60` so it persists across browser sessions (matches the 30-day expiry).
- Flash cookie must set `HttpOnly: true` (no reason for JS to access it).

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/auth/auth.go` | CreateSessionCookie, ValidateSession, ClearSessionCookie, RequireAuth middleware, context helpers |
| `internal/auth/auth_test.go` | Tests for all auth functions |
| `internal/flash/flash.go` | Set (write signed cookie), Get (read + clear signed cookie) |
| `internal/flash/flash_test.go` | Tests for flash Set/Get |
| `internal/store/store.go` | Modify: export PasswordHash field on User |

---

## API Design

### Auth package

```go
// Cookie name constant
const CookieName = "dsforms_session"

// CreateSessionCookie creates a signed session cookie for the given user.
// Cookie value: base64(userID + ":" + timestamp + ":" + hmac)
// Secure flag set if baseURL starts with "https".
func CreateSessionCookie(userID, secretKey, baseURL string) *http.Cookie

// ValidateSession extracts and validates the session cookie from the request.
// Returns the userID if valid, ok=false if missing/tampered/expired.
// Sessions expire after 30 days.
func ValidateSession(r *http.Request, secretKey string) (userID string, ok bool)

// ClearSessionCookie returns a cookie that clears the session.
func ClearSessionCookie() *http.Cookie

// RequireAuth middleware validates session, loads User, stores in context.
// Redirects to /admin/login if invalid.
type UserStore interface {
    GetUserByID(id string) (store.User, error)
}
func RequireAuth(us UserStore, secretKey string) func(http.Handler) http.Handler

// Context helpers
type contextKey string
func UserFromContext(ctx context.Context) (store.User, bool)
```

### Flash package

```go
const CookieName = "dsforms_flash"

// Set writes a signed flash cookie with msgType and message.
func Set(w http.ResponseWriter, secretKey, msgType, message string)

// Get reads the flash cookie, clears it, and returns msgType + message.
// Returns empty strings if missing or tampered.
func Get(r *http.Request, w http.ResponseWriter, secretKey string) (msgType, message string)
```

---

### Task 1: Export PasswordHash on User struct

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Rename `passwordHash` to `PasswordHash` in User struct**

In store.go, change the User struct field from `passwordHash string` (unexported) to `PasswordHash string` (exported).

Update all references in store.go that use `passwordHash` to use `PasswordHash`.

- [ ] **Step 2: Update store_test.go references**

Change `u.passwordHash` to `u.PasswordHash` in TestDefaultUserNotReseeded and TestCreateUserBcryptsPassword.

- [ ] **Step 3: Run tests — confirm PASS**

```bash
go test ./internal/store/... -v -count=1
```

- [ ] **Step 4: Commit**

```bash
git add internal/store/
git commit -m "refactor: export PasswordHash field on User for auth package access"
```

---

### Task 2: Write auth tests (red phase) + implement (green phase)

**Files:**
- Create: `internal/auth/auth.go`
- Create: `internal/auth/auth_test.go`

- [ ] **Step 1: Write auth tests**

```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testSecret = "test-secret-key-32-chars-long!!"

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

func TestCreateSessionCookieContainsUserID(t *testing.T) {
	t.Parallel()
	c := CreateSessionCookie("user-123", testSecret, "https://example.com")
	if c.Value == "" {
		t.Fatal("cookie value is empty")
	}
	// Validate by parsing it back
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
	// We need to create a cookie with an old timestamp.
	// Use createSessionValue directly with a past time.
	val := createSessionValue("user-123", testSecret, time.Now().Add(-31*24*time.Hour))
	c := &http.Cookie{Name: CookieName, Value: val}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	_, ok := ValidateSession(req, testSecret)
	if ok {
		t.Fatal("ValidateSession returned ok=true for expired cookie")
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
```

Note: `createSessionValue` is an unexported helper that takes an explicit timestamp — needed to test expiration without time.Sleep.

- [ ] **Step 2: Write RequireAuth tests**

```go
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
```

```go
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
```

Need import: `"fmt"` in test file.

Mock store for tests (in test file):
```go
type mockUserStore struct {
	user store.User
	err  error
}

func (m *mockUserStore) GetUserByID(id string) (store.User, error) {
	if m.err != nil {
		return store.User{}, m.err
	}
	return m.user, nil
}
```

Need import: `"github.com/youruser/dsforms/internal/store"`

- [ ] **Step 3: Create stub auth.go, run tests — confirm FAIL**

- [ ] **Step 4: Implement auth.go**

Key implementation:
- `createSessionValue(userID, secretKey string, t time.Time) string`: encode `userID:unixTimestamp:hmac` as base64
- `CreateSessionCookie`: calls createSessionValue with time.Now(), sets cookie attributes
- `ValidateSession`: decode base64, split on ":", verify HMAC, check timestamp < 30 days
- `ClearSessionCookie`: returns cookie with MaxAge=-1
- `RequireAuth`: validates session, calls store.GetUserByID, puts User in context
- `UserFromContext`: type-asserts from context

- [ ] **Step 5: Run tests — confirm PASS**

```bash
go test ./internal/auth/... -v -count=1
go test -race ./internal/auth/... -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/auth/
git commit -m "feat: add auth package with session cookies and RequireAuth middleware"
```

---

### Task 3: Write flash tests (red phase) + implement (green phase)

**Files:**
- Create: `internal/flash/flash.go`
- Create: `internal/flash/flash_test.go`

- [ ] **Step 1: Write flash tests**

```go
package flash

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testSecret = "test-secret-key-32-chars-long!!"

func TestSetWritesCookie(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	Set(w, testSecret, "success", "Password updated.")
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == CookieName {
			found = true
			if c.Value == "" {
				t.Error("cookie value is empty")
			}
		}
	}
	if !found {
		t.Fatal("flash cookie not set")
	}
}

func TestGetReadsAndClears(t *testing.T) {
	t.Parallel()
	// Set the flash
	w1 := httptest.NewRecorder()
	Set(w1, testSecret, "success", "Done!")
	cookies := w1.Result().Cookies()

	// Read the flash
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	msgType, message := Get(req, w2, testSecret)
	if msgType != "success" {
		t.Errorf("msgType = %q, want %q", msgType, "success")
	}
	if message != "Done!" {
		t.Errorf("message = %q, want %q", message, "Done!")
	}
	// Verify cookie is cleared (MaxAge=-1)
	clearCookies := w2.Result().Cookies()
	found := false
	for _, c := range clearCookies {
		if c.Name == CookieName && c.MaxAge == -1 {
			found = true
		}
	}
	if !found {
		t.Error("flash cookie not cleared after Get")
	}
}

func TestGetMissingCookie(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	msgType, message := Get(req, w, testSecret)
	if msgType != "" || message != "" {
		t.Errorf("expected empty strings, got %q %q", msgType, message)
	}
}

func TestGetTamperedCookie(t *testing.T) {
	t.Parallel()
	w1 := httptest.NewRecorder()
	Set(w1, testSecret, "error", "Bad thing happened")
	cookies := w1.Result().Cookies()

	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		c.Value = c.Value + "tampered"
		req.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	msgType, message := Get(req, w2, testSecret)
	if msgType != "" || message != "" {
		t.Errorf("expected empty strings for tampered, got %q %q", msgType, message)
	}
}

func TestFlashRoundTripTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		msgType string
		message string
	}{
		{"success", "Password updated."},
		{"error", "Something went wrong."},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.msgType, func(t *testing.T) {
			t.Parallel()
			w1 := httptest.NewRecorder()
			Set(w1, testSecret, tt.msgType, tt.message)
			req := httptest.NewRequest("GET", "/", nil)
			for _, c := range w1.Result().Cookies() {
				req.AddCookie(c)
			}
			w2 := httptest.NewRecorder()
			gotType, gotMsg := Get(req, w2, testSecret)
			if gotType != tt.msgType {
				t.Errorf("msgType = %q, want %q", gotType, tt.msgType)
			}
			if gotMsg != tt.message {
				t.Errorf("message = %q, want %q", gotMsg, tt.message)
			}
		})
	}
}
```

- [ ] **Step 2: Create stub flash.go, run tests — confirm FAIL**

- [ ] **Step 3: Implement flash.go**

Implementation:
- `Set`: encode `msgType:message` as base64, compute HMAC, cookie value = `base64payload.hmacHex`, set cookie with Path="/"
- `Get`: read cookie, split on ".", verify HMAC, decode base64, split on first ":" to get msgType and message, then clear cookie

- [ ] **Step 4: Run tests — confirm PASS**

```bash
go test ./internal/flash/... -v -count=1
go test -race ./internal/flash/... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/flash/
git commit -m "feat: add flash package with signed cookie messages"
```

---

### Task 4: Final verification

- [ ] **Step 1: Full checks**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

All must exit 0.

- [ ] **Step 2: Verify no hardcoded cookie values in tests**

```bash
grep -r "dsforms_session=" internal/auth/
grep -r "dsforms_flash=" internal/flash/
```

Expected: no matches (cookies are always derived from functions, never hardcoded).
