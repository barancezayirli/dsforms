# Session 6 — Auth Handlers & Login UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Login page, login POST with bcrypt verification and LoginGuard brute-force protection, logout, RequireAuth wired on all admin routes, base.html template shell with nav/warning banner/flash messages.

**Architecture:** `internal/handler/auth.go` holds an `AuthHandler` struct with LoginPage, LoginSubmit, Logout methods. It uses `store.CheckPassword` for credential verification, `auth.CreateSessionCookie`/`ClearSessionCookie` for session management, and `ratelimit.LoginGuard` for brute-force protection. Templates are embedded via `//go:embed templates/*` and parsed once at startup. `base.html` is the shared layout with nav, warning banner, and flash rendering. `login.html` extends base.html for the login page.

**Tech Stack:** Go `html/template`, `//go:embed`, chi router, existing auth/store/ratelimit/flash packages.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `templates/base.html` | Shared layout: HTML head with CSS tokens, nav bar, warning banner, flash messages, content block, burger JS |
| `templates/login.html` | Login form (extends base.html but without nav — standalone centered card) |
| `internal/handler/auth.go` | AuthHandler: LoginPage, LoginSubmit, Logout |
| `internal/handler/auth_test.go` | 10 tests for auth handlers |
| `main.go` | Embed templates, parse once, wire auth routes + RequireAuth group + LoginGuard |

---

## Key Design Decisions

- `login.html` does NOT extend `base.html` — it's a standalone page (no nav, no warning banner). The login page has its own minimal layout with just the form centered.
- `base.html` defines blocks: `{{define "base"}}...{{template "content" .}}...{{end}}`. Admin pages define `{{define "content"}}...{{end}}`.
- Templates are embedded in `main.go` with `//go:embed templates/*` and parsed once into a `*template.Template`.
- The AuthHandler needs: store (for CheckPassword), secretKey, baseURL, loginGuard, templates.
- Flash messages are read in the handler (not middleware) and passed to the template data.

---

### Task 1: Create templates (base.html + login.html)

**Files:**
- Create: `templates/base.html`
- Create: `templates/login.html`

- [ ] **Step 1: Create base.html**

Full layout template per DSFORMS_FRONTEND.md §1-3. Includes:
- HTML5 doctype, viewport meta
- All CSS design tokens from §1
- Global reset from §2
- Nav bar from §3.1 (with burger menu)
- Warning banner from §3.2 (conditional on .CurrentUser.IsDefaultPassword)
- Flash message from §3.3 (conditional on .Flash)
- Page wrapper from §3.4
- Content block: `{{template "content" .}}`
- Burger JS from §3.1

Template data expected:
```go
type PageData struct {
    Title       string
    Active      string  // "forms", "users", "backups"
    CurrentUser store.User
    Flash       *FlashData
    // ... page-specific fields embedded by each page
}

type FlashData struct {
    Type    string  // "success" or "error"
    Message string
}
```

- [ ] **Step 2: Create login.html**

Standalone login page (not extending base.html). Centered card with:
- DSForms branding
- Username + password inputs
- Submit button
- Error message (conditional on .LoginError)
- Same CSS tokens but minimal — just what's needed for login

Template data:
```go
type LoginData struct {
    LoginError bool
}
```

- [ ] **Step 3: Commit**

```bash
git add templates/
git commit -m "feat: add base.html and login.html templates"
```

---

### Task 2: Auth handler tests (red phase) + implementation (green phase)

**Files:**
- Create: `internal/handler/auth.go`
- Create: `internal/handler/auth_test.go`

- [ ] **Step 1: Write all 10 auth handler tests**

```go
package handler

import (
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
    "html/template"
)

func setupAuth(t *testing.T) (*store.Store, *chi.Mux) {
    t.Helper()
    s, err := store.New(":memory:")
    if err != nil {
        t.Fatalf("store.New error: %v", err)
    }
    now := time.Now()
    guard := ratelimit.NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })

    // Parse templates — use a minimal inline template for testing
    // (avoids fragile relative paths; real templates tested via integration in main.go)
    tmpl := template.Must(template.New("login.html").Parse(`<!DOCTYPE html><html><body>
    {{if .LoginError}}<p>Invalid username or password</p>{{end}}
    <form method="POST" action="/admin/login">
    <input name="username"><input name="password" type="password">
    <button type="submit">Log in</button></form></body></html>`))

    ah := &AuthHandler{
        Store:      s,
        SecretKey:  "test-secret-key-32-chars-long!!",
        BaseURL:    "https://example.com",
        LoginGuard: guard,
        Templates:  tmpl,
    }

    r := chi.NewRouter()
    r.Get("/admin/login", ah.LoginPage)
    r.Post("/admin/login", ah.LoginSubmit)
    r.Post("/admin/logout", ah.Logout)

    // Protected route for testing RequireAuth
    r.Group(func(r chi.Router) {
        r.Use(auth.RequireAuth(s, "test-secret-key-32-chars-long!!"))
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
    // Check cookie is set
    cookies := w.Result().Cookies()
    found := false
    for _, c := range cookies {
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
    // No session cookie should be set
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
    // No session cookie should be set
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
    // 6th attempt should be locked
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
    // Check cookie is cleared
    cookies := w.Result().Cookies()
    for _, c := range cookies {
        if c.Name == auth.CookieName && c.MaxAge == -1 {
            return // good
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
    cookie := auth.CreateSessionCookie(admin.ID, "test-secret-key-32-chars-long!!", "https://example.com")
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
    cookie := auth.CreateSessionCookie(admin.ID, "test-secret-key-32-chars-long!!", "https://example.com")
    cookie.Value += "tampered"
    req := httptest.NewRequest("GET", "/admin/forms", nil)
    req.AddCookie(cookie)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusFound {
        t.Errorf("status = %d, want 302", w.Code)
    }
}
```

- [ ] **Step 2: Create AuthHandler struct + stub methods**

```go
type AuthHandler struct {
    Store      *store.Store
    SecretKey  string
    BaseURL    string
    LoginGuard *ratelimit.LoginGuard
    Templates  *template.Template
}

func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {}
func (h *AuthHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {}
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {}
```

- [ ] **Step 3: Run tests — confirm FAIL**

- [ ] **Step 4: Implement AuthHandler methods**

**LoginPage:** Render login.html with LoginError from ?error=1 query param.

**LoginSubmit:**
1. Check LoginGuard.IsLocked(ip) → 429 if locked
2. Parse form values (username, password)
3. Call store.CheckPassword(username, password)
4. If error: guard.RecordFailure(ip), redirect to /admin/login?error=1
5. If success: guard.RecordSuccess(ip), create session cookie, redirect to /admin/forms

**Logout:** Set clear cookie, redirect to /admin/login.

- [ ] **Step 5: Run tests — confirm PASS**

```bash
go test ./internal/handler/... -v -count=1
go test -race ./internal/handler/... -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/handler/auth.go internal/handler/auth_test.go
git commit -m "feat: add auth handlers with login, logout, and brute-force protection"
```

---

### Task 3: Wire everything in main.go

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Add template embedding and parsing**

```go
//go:embed templates/*
var templateFS embed.FS
```

Parse templates at startup in `main()`.

- [ ] **Step 2: Create AuthHandler with LoginGuard**

```go
loginGuard := ratelimit.NewLoginGuard(5, 15*time.Minute, time.Now)
loginGuard.StartCleanup(30*time.Minute, 30*time.Minute)

authHandler := &handler.AuthHandler{
    Store:      s,
    SecretKey:  cfg.SecretKey,
    BaseURL:    cfg.BaseURL,
    LoginGuard: loginGuard,
    Templates:  tmpl,
}
```

- [ ] **Step 3: Wire public auth routes**

```go
r.Get("/admin/login", authHandler.LoginPage)
r.Post("/admin/login", authHandler.LoginSubmit)
```

- [ ] **Step 4: Wire protected admin group with RequireAuth**

```go
r.Group(func(r chi.Router) {
    r.Use(auth.RequireAuth(s, cfg.SecretKey))
    r.Post("/admin/logout", authHandler.Logout)
    r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, "/admin/forms", http.StatusFound)
    })
    // Placeholder for future admin routes (Sessions 7-10)
    r.Get("/admin/forms", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("dashboard placeholder"))
    })
})
```

- [ ] **Step 5: Run full test suite**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

- [ ] **Step 6: Commit**

```bash
git add main.go templates/
git commit -m "feat: wire auth routes, RequireAuth middleware, and template embedding"
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
