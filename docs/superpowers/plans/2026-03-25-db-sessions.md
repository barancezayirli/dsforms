# DB-Backed Session Tokens Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace HMAC-signed session cookies with DB-backed random tokens (SHA-256 hashed in storage) for session revocation, password-change logout, and open-source credibility.

**Architecture:** Store adds `sessions` table + 5 methods. Auth package simplifies to token read/write (no crypto). Handler updates login/logout to use store sessions. All existing auth tests updated. This is a refactor — no new features, just a better session mechanism.

**Tech Stack:** Go stdlib (`crypto/rand`, `crypto/sha256`, `encoding/hex`), existing SQLite store.

**Spec:** `docs/superpowers/specs/2026-03-25-db-sessions-design.md`

---

## File Structure

| File | Change |
|------|--------|
| `internal/store/store.go` | Add sessions table migration + 5 session methods |
| `internal/store/store_test.go` | Tests for all 5 session methods |
| `internal/auth/auth.go` | Replace HMAC with token-based functions, new SessionStore interface |
| `internal/auth/auth_test.go` | Rewrite tests for new API |
| `internal/handler/auth.go` | Update LoginSubmit + Logout to use store sessions |
| `internal/handler/auth_test.go` | Update auth handler tests |
| `main.go` | Update RequireAuth call, add session cleanup goroutine |

---

### Task 1: Add sessions table + store methods (TDD)

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Add sessions table to migration in New()**

Add to the schema SQL in `New()`:
```sql
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
```

- [ ] **Step 2: Write tests for all 5 session methods**

```go
func TestCreateSession(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    token, err := s.CreateSession(admin.ID, 30*24*time.Hour)
    if err != nil { t.Fatalf("error = %v", err) }
    if len(token) != 64 { t.Errorf("token len = %d, want 64 (hex of 32 bytes)", len(token)) }
}

func TestGetSessionValid(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
    userID, err := s.GetSession(token)
    if err != nil { t.Fatalf("error = %v", err) }
    if userID != admin.ID { t.Errorf("userID = %q, want %q", userID, admin.ID) }
}

func TestGetSessionExpired(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    token, _ := s.CreateSession(admin.ID, -1*time.Hour) // already expired
    _, err := s.GetSession(token)
    if err == nil { t.Fatal("expected error for expired session") }
}

func TestGetSessionNotFound(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _, err := s.GetSession("nonexistent-token-hex-string")
    if err == nil { t.Fatal("expected error for nonexistent token") }
}

func TestDeleteSession(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
    err := s.DeleteSession(token)
    if err != nil { t.Fatalf("error = %v", err) }
    _, err = s.GetSession(token)
    if err == nil { t.Fatal("session still valid after delete") }
}

func TestDeleteUserSessions(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    s.CreateSession(admin.ID, 30*24*time.Hour)
    s.CreateSession(admin.ID, 30*24*time.Hour)
    err := s.DeleteUserSessions(admin.ID)
    if err != nil { t.Fatalf("error = %v", err) }
    // Both sessions should be gone — create a new one to verify table works
    token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
    _, err = s.GetSession(token)
    if err != nil { t.Fatalf("new session after delete failed: %v", err) }
}

func TestCleanExpiredSessions(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    s.CreateSession(admin.ID, -1*time.Hour) // expired
    validToken, _ := s.CreateSession(admin.ID, 30*24*time.Hour) // valid
    err := s.CleanExpiredSessions()
    if err != nil { t.Fatalf("error = %v", err) }
    // Valid session should still work
    _, err = s.GetSession(validToken)
    if err != nil { t.Fatalf("valid session gone after cleanup: %v", err) }
}

func TestCreateSessionEmptyUserID(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _, err := s.CreateSession("", 30*24*time.Hour)
    if err == nil { t.Fatal("expected error for empty userID") }
}
```

- [ ] **Step 3: Implement all 5 session methods**

```go
import (
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
)

func hashToken(token string) string {
    h := sha256.Sum256([]byte(token))
    return hex.EncodeToString(h[:])
}

func (s *Store) CreateSession(userID string, expiry time.Duration) (string, error) {
    if userID == "" {
        return "", fmt.Errorf("create session: userID must not be empty")
    }
    b := make([]byte, 32)
    if _, err := rand.Read(b); err != nil {
        return "", fmt.Errorf("create session: %w", err)
    }
    token := hex.EncodeToString(b)
    tokenHash := hashToken(token)
    expiresAt := time.Now().Add(expiry)
    _, err := s.db.Exec(
        "INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
        tokenHash, userID, expiresAt,
    )
    if err != nil {
        return "", fmt.Errorf("create session: %w", err)
    }
    return token, nil
}

func (s *Store) GetSession(token string) (string, error) {
    tokenHash := hashToken(token)
    var userID string
    err := s.db.QueryRow(
        "SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > datetime('now')",
        tokenHash,
    ).Scan(&userID)
    if err != nil {
        return "", fmt.Errorf("get session: %w", err)
    }
    return userID, nil
}

func (s *Store) DeleteSession(token string) error {
    tokenHash := hashToken(token)
    _, err := s.db.Exec("DELETE FROM sessions WHERE token_hash = ?", tokenHash)
    if err != nil {
        return fmt.Errorf("delete session: %w", err)
    }
    return nil
}

func (s *Store) DeleteUserSessions(userID string) error {
    _, err := s.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
    if err != nil {
        return fmt.Errorf("delete user sessions: %w", err)
    }
    return nil
}

func (s *Store) CleanExpiredSessions() error {
    _, err := s.db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
    if err != nil {
        return fmt.Errorf("clean expired sessions: %w", err)
    }
    return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/store/... -v -count=1 -run "TestCreateSession|TestGetSession|TestDeleteSession|TestDeleteUserSessions|TestCleanExpired"
go test -race ./internal/store/... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "feat: add sessions table with token-hashed DB-backed session methods"
```

---

### Task 2: Refactor auth package (TDD)

**Files:**
- Modify: `internal/auth/auth.go`
- Modify: `internal/auth/auth_test.go`

- [ ] **Step 1: Rewrite auth_test.go FIRST (TDD red phase)**

Write the new tests before touching the implementation. These tests will fail against the old HMAC auth.go because the API has changed.

```go
package auth

import (
    "context"
    "log"
    "net/http"
    "strings"

    "github.com/youruser/dsforms/internal/store"
)

const CookieName = "dsforms_session"

type contextKey string
const userContextKey contextKey = "user"

// SessionStore is the interface the auth package needs.
type SessionStore interface {
    GetSession(token string) (userID string, err error)
    GetUserByID(id string) (store.User, error)
}

// CreateSessionCookie wraps a session token in an HTTP cookie.
func CreateSessionCookie(token, baseURL string) *http.Cookie {
    return &http.Cookie{
        Name:     CookieName,
        Value:    token,
        Path:     "/",
        MaxAge:   30 * 24 * 60 * 60,
        HttpOnly: true,
        SameSite: http.SameSiteLaxMode,
        Secure:   strings.HasPrefix(baseURL, "https"),
    }
}

// GetSessionToken reads the session token from the request cookie.
func GetSessionToken(r *http.Request) (string, bool) {
    c, err := r.Cookie(CookieName)
    if err != nil || c.Value == "" {
        return "", false
    }
    return c.Value, true
}

// ClearSessionCookie returns a cookie that clears the session.
func ClearSessionCookie() *http.Cookie {
    return &http.Cookie{
        Name:     CookieName,
        Value:    "",
        Path:     "/",
        MaxAge:   -1,
        HttpOnly: true,
    }
}

// RequireAuth validates the session token via DB lookup.
func RequireAuth(ss SessionStore) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            token, ok := GetSessionToken(r)
            if !ok {
                http.Redirect(w, r, "/admin/login", http.StatusFound)
                return
            }
            userID, err := ss.GetSession(token)
            if err != nil {
                http.SetCookie(w, ClearSessionCookie())
                http.Redirect(w, r, "/admin/login", http.StatusFound)
                return
            }
            user, err := ss.GetUserByID(userID)
            if err != nil {
                log.Printf("auth: failed to load user %s: %v", userID, err)
                http.SetCookie(w, ClearSessionCookie())
                http.Redirect(w, r, "/admin/login", http.StatusFound)
                return
            }
            ctx := context.WithValue(r.Context(), userContextKey, user)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) (store.User, bool) {
    u, ok := ctx.Value(userContextKey).(store.User)
    return u, ok
}
```

- [ ] **Step 2: Confirm tests FAIL against old auth.go** (API mismatch = compile error)

- [ ] **Step 3: Rewrite auth.go (TDD green phase)**

Replace the HMAC implementation with the token-based one:

```go
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
    if !c.HttpOnly { t.Error("HttpOnly = false") }
    if c.SameSite != http.SameSiteLaxMode { t.Error("SameSite != Lax") }
    if !c.Secure { t.Error("Secure = false for https") }
    if c.Path != "/" { t.Errorf("Path = %q", c.Path) }
    if c.MaxAge != 30*24*60*60 { t.Errorf("MaxAge = %d", c.MaxAge) }
    if c.Value != "abc123token" { t.Errorf("Value = %q", c.Value) }
}

func TestCreateSessionCookieSecureFalseHTTP(t *testing.T) {
    t.Parallel()
    c := CreateSessionCookie("token", "http://localhost")
    if c.Secure { t.Error("Secure = true for http") }
}

func TestGetSessionToken(t *testing.T) {
    t.Parallel()
    req := httptest.NewRequest("GET", "/", nil)
    req.AddCookie(&http.Cookie{Name: CookieName, Value: "mytoken"})
    token, ok := GetSessionToken(req)
    if !ok { t.Fatal("ok = false") }
    if token != "mytoken" { t.Errorf("token = %q", token) }
}

func TestGetSessionTokenMissing(t *testing.T) {
    t.Parallel()
    req := httptest.NewRequest("GET", "/", nil)
    _, ok := GetSessionToken(req)
    if ok { t.Fatal("ok = true for missing cookie") }
}

func TestClearSessionCookie(t *testing.T) {
    t.Parallel()
    c := ClearSessionCookie()
    if c.MaxAge != -1 { t.Errorf("MaxAge = %d", c.MaxAge) }
    if c.Name != CookieName { t.Errorf("Name = %q", c.Name) }
}

func TestRequireAuthValidSession(t *testing.T) {
    t.Parallel()
    s, _ := store.New(":memory:")
    admin, _ := s.GetUserByUsername("admin")
    token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)

    handler := RequireAuth(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u, ok := UserFromContext(r.Context())
        if !ok { t.Fatal("no user in context") }
        if u.Username != "admin" { t.Errorf("Username = %q", u.Username) }
        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest("GET", "/admin/forms", nil)
    req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    if w.Code != http.StatusOK { t.Errorf("status = %d, want 200", w.Code) }
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
    if w.Code != http.StatusFound { t.Errorf("status = %d, want 302", w.Code) }
    // Should clear the stale cookie
    for _, c := range w.Result().Cookies() {
        if c.Name == CookieName && c.MaxAge == -1 { return }
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
    if w.Code != http.StatusFound { t.Errorf("status = %d, want 302", w.Code) }
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
    if w.Code != http.StatusFound { t.Errorf("status = %d, want 302", w.Code) }
}
```

Note: auth tests depend on store.CreateSession working, so Task 1 MUST be complete before Task 2.

- [ ] **Step 4: Run auth tests**

```bash
go test ./internal/auth/... -v -count=1
go test -race ./internal/auth/... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/auth/
git commit -m "refactor: replace HMAC auth with DB-backed session tokens"
```

---

### Task 3: Update handler + main.go

**Files:**
- Modify: `internal/handler/auth.go`
- Modify: `internal/handler/auth_test.go`
- Modify: `internal/handler/admin_test.go`
- Modify: `main.go`

- [ ] **Step 1: Update auth handler**

In `internal/handler/auth.go`:

**LoginSubmit** — replace `auth.CreateSessionCookie(user.ID, h.SecretKey, h.BaseURL)` with:
```go
token, err := h.Store.CreateSession(user.ID, 30*24*time.Hour)
if err != nil {
    log.Printf("login: failed to create session: %v", err)
    http.Redirect(w, r, "/admin/login?error=1", http.StatusFound)
    return
}
cookie := auth.CreateSessionCookie(token, h.BaseURL)
```

**Logout** — replace `auth.ClearSessionCookie()` with:
```go
token, _ := auth.GetSessionToken(r)
if token != "" {
    h.Store.DeleteSession(token)
}
http.SetCookie(w, auth.ClearSessionCookie())
```

- [ ] **Step 2: Update auth handler tests**

In `internal/handler/auth_test.go`, update `setupAuth`:
- `RequireAuth` now takes just the store: `auth.RequireAuth(s)` (not `auth.RequireAuth(s, testSecretKey)`)
- Login tests: instead of checking for a specific cookie format, just verify the cookie exists and is parseable
- Remove `testSecretKey` from RequireAuth calls

Update test helpers that create auth cookies for admin requests:
- Instead of `auth.CreateSessionCookie(admin.ID, testSecretKey, "https://example.com")`
- Use `s.CreateSession(admin.ID, 30*24*time.Hour)` then `auth.CreateSessionCookie(token, "https://example.com")`

- [ ] **Step 3: Update admin_test.go helper**

The `doAdminRequest` helper in admin_test.go creates auth cookies. Update it to use store sessions:
```go
func doAdminRequest(t *testing.T, s *store.Store, r *chi.Mux, method, path, body string) *httptest.ResponseRecorder {
    t.Helper()
    admin, _ := s.GetUserByUsername("admin")
    token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
    cookie := auth.CreateSessionCookie(token, "https://example.com")
    // ... rest unchanged
}
```

Also update `setupAuth` and `setupAdmin` to use `auth.RequireAuth(s)` instead of `auth.RequireAuth(s, testSecretKey)`.

- [ ] **Step 4: Update main.go**

- Change `auth.RequireAuth(s, cfg.SecretKey)` to `auth.RequireAuth(s)`
- Add session cleanup goroutine after store creation:
```go
go func() {
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        if err := s.CleanExpiredSessions(); err != nil {
            log.Printf("session cleanup error: %v", err)
        }
    }
}()
```

- [ ] **Step 5: Run ALL tests**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

ALL must pass.

- [ ] **Step 6: Commit**

```bash
git add internal/handler/ main.go
git commit -m "refactor: update handlers and main.go for DB-backed sessions"
```

---

### Task 4: Final verification

- [ ] **Step 1: Full checks**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

- [ ] **Step 2: Verify no HMAC code remains in auth package**

```bash
grep -r "hmac\|sha256\|base64\|secretKey\|SecretKey" internal/auth/
```

Expected: no matches (all HMAC code removed from auth). SecretKey should only appear in handler/admin.go for flash, not in auth.
