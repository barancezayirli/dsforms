# DB-Backed Session Tokens — Design Spec

## Problem

The current HMAC-signed cookie approach works but lacks session revocation, which is expected by the open-source community. Password changes cannot invalidate existing sessions, and compromised cookies remain valid for 30 days.

## Solution

Replace HMAC-signed cookies with random session tokens stored in a `sessions` table in SQLite. Each login creates a row, each request validates by DB lookup, logout/password-change deletes rows. Tokens are hashed (SHA-256) before storage so a DB leak does not expose valid session tokens.

## Schema

```sql
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,  -- SHA-256 hash of the raw token
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
```

- `ON DELETE CASCADE`: deleting a user automatically cleans up their sessions.
- Expiry is stored per-session (30 days from creation).

## Store Methods

```go
// CreateSession generates a random token, stores SHA-256(token) in DB, returns raw token.
// Token: 32 random bytes, hex-encoded (64 characters).
// Stored: SHA-256 hash of the token (not the raw token).
// Expiry: created_at + duration (30 days).
func (s *Store) CreateSession(userID string, expiry time.Duration) (string, error)

// GetSession hashes the provided token, looks it up in DB.
// Returns userID if valid and not expired.
// Uses SQL filter: WHERE token_hash = ? AND expires_at > datetime('now')
// Returns sql.ErrNoRows if token not found or expired.
func (s *Store) GetSession(token string) (string, error)

// DeleteSession removes a single session (logout).
func (s *Store) DeleteSession(token string) error

// DeleteUserSessions removes ALL sessions for a user (password change, user delete).
func (s *Store) DeleteUserSessions(userID string) error

// CleanExpiredSessions removes all expired session rows.
func (s *Store) CleanExpiredSessions() error
```

## Auth Package Changes

### Before (HMAC)
```go
func CreateSessionCookie(userID, secretKey, baseURL string) *http.Cookie
func ValidateSession(r *http.Request, secretKey string) (userID string, ok bool)
func ClearSessionCookie() *http.Cookie
func RequireAuth(us UserStore, secretKey string) func(http.Handler) http.Handler
```

### After (DB tokens)
```go
// SessionStore is the interface the auth package needs.
type SessionStore interface {
    GetSession(token string) (userID string, err error)
    GetUserByID(id string) (store.User, error)
}

func CreateSessionCookie(token, baseURL string) *http.Cookie
func GetSessionToken(r *http.Request) (token string, ok bool)
func ClearSessionCookie() *http.Cookie
func RequireAuth(ss SessionStore) func(http.Handler) http.Handler
```

Key simplifications:
- `CreateSessionCookie` takes a token string (not userID + secretKey) — just sets the cookie.
- `GetSessionToken` replaces `ValidateSession` — just reads the cookie value, no crypto.
- `RequireAuth` takes only `SessionStore` — no secretKey needed.
- All HMAC code removed.

## Cookie Attributes (unchanged)

- Name: `dsforms_session`
- Value: hex token (64 chars)
- Path: `/`
- MaxAge: 30 * 24 * 60 * 60
- HttpOnly: true
- SameSite: Lax
- Secure: true if baseURL starts with "https"

Note: `baseURL` is still needed for the Secure flag. Pass it when creating the cookie in the handler.

## Handler Changes

### LoginSubmit (auth handler)
```
Before: user, err := store.CheckPassword(username, password)
         cookie := auth.CreateSessionCookie(user.ID, secretKey, baseURL)

After:  user, err := store.CheckPassword(username, password)
        token, err := store.CreateSession(user.ID, 30*24*time.Hour)
        cookie := auth.CreateSessionCookie(token, baseURL)
```

### Logout (auth handler)
```
Before: http.SetCookie(w, auth.ClearSessionCookie())

After:  token, _ := auth.GetSessionToken(r)
        store.DeleteSession(token)
        http.SetCookie(w, auth.ClearSessionCookie())
```

### UpdatePassword (Session 9 — future)
```
After password update:
  store.DeleteUserSessions(userID)
```

## RequireAuth Middleware

```go
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
                http.SetCookie(w, ClearSessionCookie()) // clear stale cookie
                http.Redirect(w, r, "/admin/login", http.StatusFound)
                return
            }
            user, err := ss.GetUserByID(userID)
            if err != nil {
                log.Printf("auth: failed to load user %s: %v", userID, err)
                http.Redirect(w, r, "/admin/login", http.StatusFound)
                return
            }
            ctx := context.WithValue(r.Context(), userContextKey, user)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

## Cleanup

Expired sessions accumulate in the DB. Add periodic cleanup:

```go
// In main.go, after store is opened:
go func() {
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        if err := s.CleanExpiredSessions(); err != nil {
            log.Printf("session cleanup error: %v", err)
        }
    }
}()
```

## Files Changed

| File | Change |
|------|--------|
| `internal/store/store.go` | Add sessions table to migration, add 5 session methods |
| `internal/store/store_test.go` | Tests for all 5 session methods |
| `internal/auth/auth.go` | Replace HMAC with token-based functions, new SessionStore interface |
| `internal/auth/auth_test.go` | Update all tests for new API |
| `internal/handler/auth.go` | Update LoginSubmit and Logout to use store sessions |
| `internal/handler/auth_test.go` | Update tests for new auth API |
| `main.go` | Remove secretKey from auth calls, add session cleanup goroutine |

## Security Properties

- **Session revocation**: delete from DB → immediate invalidation
- **Password change**: deletes all sessions → forces re-login everywhere
- **User deletion**: CASCADE deletes sessions automatically
- **Token entropy**: 32 random bytes = 256 bits (cryptographically secure via `crypto/rand`)
- **No secrets in cookie**: token is opaque, no user data encoded
- **Token hashing**: DB stores SHA-256(token), not raw token — DB leak does not expose valid sessions
- **DB lookup**: no client-side crypto needed, just hash + SQL query

## Breaking Change

This is a one-time breaking change. Existing HMAC-signed cookies will fail validation (the middleware reads a hex token, not a base64 HMAC payload) and users will be redirected to login. No migration code is needed — old cookies fail gracefully. All active users must re-login once after deploy.

## What This Does NOT Change

- `SECRET_KEY` env var — still required for flash cookie HMAC signing (`flash.Set`/`flash.Get`). It is no longer used by auth.
- `AuthHandler.SecretKey` and `AdminHandler.SecretKey` fields — retained for flash messages
- Flash cookies (still HMAC-signed — they're short-lived and contain no sensitive data)
- `UserFromContext` — unchanged, still reads User from request context
- Cookie attributes (HttpOnly, SameSite, Secure, MaxAge)
- The login/logout UX flow
- Rate limiting / LoginGuard
