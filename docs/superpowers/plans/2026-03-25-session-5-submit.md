# Session 5 — Submit Handler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `POST /f/{formID}` fully working — honeypot check, rate limiting, email dispatch (async), redirect/JSON response, all tested with mock mailer and in-memory store.

**Architecture:** `internal/mail` defines a `Notifier` interface + real `Mailer` struct + `MockMailer` for tests. `internal/handler` has a `SubmitHandler` struct that takes a store and notifier interface. The handler follows the 9-step flow from DSFORMS_PLAN.md §6. Rate limiter middleware is wired in `main.go` on the submit route only. Email is sent in a goroutine so it doesn't block the response.

**Tech Stack:** Go stdlib (`net/smtp`, `net/http`), chi router, existing store/ratelimit packages. No new dependencies.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/mail/mail.go` | Notifier interface, Mailer struct (SMTP), MockMailer for tests |
| `internal/mail/mail_test.go` | Test Mailer against a local test SMTP server |
| `internal/handler/submit.go` | SubmitHandler struct + Handle method (POST /f/{formID}) |
| `internal/handler/submit_test.go` | 15 tests covering all submit scenarios |
| `main.go` | Wire POST /f/{formID} route with rate limiter middleware |

---

## API Design

### Mail package

```go
// Notifier is the interface for sending form submission notifications.
type Notifier interface {
    SendNotification(form store.Form, sub store.Submission) error
}

// Mailer implements Notifier using SMTP.
type Mailer struct {
    Host    string
    Port    int
    User    string
    Pass    string
    From    string
    BaseURL string
}

// SendNotification sends an email notification for a new submission.
func (m *Mailer) SendNotification(form store.Form, sub store.Submission) error

// MockMailer records calls for testing.
type MockMailer struct {
    mu    sync.Mutex
    Calls []MockCall
}

type MockCall struct {
    Form store.Form
    Sub  store.Submission
}

func (m *MockMailer) SendNotification(form store.Form, sub store.Submission) error
func (m *MockMailer) CallCount() int
func (m *MockMailer) Wait(timeout time.Duration) bool  // blocks until at least 1 call, returns false on timeout
```

The MockMailer uses a channel internally to signal when SendNotification is called, allowing deterministic tests without time.Sleep:

```go
type MockMailer struct {
    mu    sync.Mutex
    Calls []MockCall
    ch    chan struct{}
}

func NewMockMailer() *MockMailer {
    return &MockMailer{ch: make(chan struct{}, 10)}
}

func (m *MockMailer) SendNotification(form store.Form, sub store.Submission) error {
    m.mu.Lock()
    m.Calls = append(m.Calls, MockCall{Form: form, Sub: sub})
    m.mu.Unlock()
    m.ch <- struct{}{}
    return nil
}

func (m *MockMailer) Wait(timeout time.Duration) bool {
    select {
    case <-m.ch:
        return true
    case <-time.After(timeout):
        return false
    }
}
```
```

### Handler package

```go
// SubmitHandler handles POST /f/{formID} submissions.
type SubmitHandler struct {
    Store    *store.Store
    Notifier mail.Notifier
    BaseURL  string
}

// Handle processes a form submission.
func (h *SubmitHandler) Handle(w http.ResponseWriter, r *http.Request)
```

---

### Task 1: Mail package (interface + mock + basic Mailer)

**Files:**
- Create: `internal/mail/mail.go`
- Create: `internal/mail/mail_test.go`

- [ ] **Step 1: Write mail_test.go**

Test the MockMailer (it's what handler tests will use) and a basic Mailer format test:

```go
package mail

import (
    "testing"
    "time"

    "github.com/youruser/dsforms/internal/store"
)

func TestMockMailerRecordsCalls(t *testing.T) {
    t.Parallel()
    m := &MockMailer{}
    form := store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}
    sub := store.Submission{ID: "s1", FormID: "f1", Data: map[string]string{"name": "Alice"}}
    err := m.SendNotification(form, sub)
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if m.CallCount() != 1 {
        t.Errorf("CallCount = %d, want 1", m.CallCount())
    }
}

func TestBuildMessage(t *testing.T) {
    t.Parallel()
    m := &Mailer{
        From:    "DSForms <noreply@example.com>",
        BaseURL: "https://forms.example.com",
    }
    form := store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}
    sub := store.Submission{
        ID:        "s1",
        FormID:    "f1",
        Data:      map[string]string{"name": "Alice", "email": "alice@example.com"},
        IP:        "1.2.3.4",
        CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
    }
    msg := m.buildMessage(form, sub)
    if msg == "" {
        t.Fatal("message is empty")
    }
    // Check key content
    tests := []struct {
        name string
        want string
    }{
        {"subject", "Subject: [DSForms] New submission: Contact"},
        {"to", "To: me@example.com"},
        {"form name", "Form:      Contact"},
        {"field name", "name:    Alice"},
        {"view link", "https://forms.example.com/admin/forms/f1"},
    }
    for _, tt := range tests {
        tt := tt
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            if !contains(msg, tt.want) {
                t.Errorf("message does not contain %q", tt.want)
            }
        })
    }
}

func contains(s, substr string) bool {
    return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
    for i := 0; i <= len(s)-len(sub); i++ {
        if s[i:i+len(sub)] == sub {
            return true
        }
    }
    return false
}
```

Actually, use `strings.Contains` instead of a custom helper — add `"strings"` to imports.

```go
func contains(s, substr string) bool {
    return strings.Contains(s, substr)
}
```

- [ ] **Step 2: Implement mail.go**

Key points:
- `Notifier` interface with `SendNotification(form, sub) error`
- `Mailer` struct with SMTP fields + `buildMessage` (unexported, builds RFC 2822 plain text email)
- `SendNotification` calls `buildMessage` then sends via `net/smtp.SendMail` (or `tls.Dial` for port 465)
- `MockMailer` records calls in a thread-safe slice
- `buildMessage` formats per DSFORMS_PLAN.md §6 email format

- [ ] **Step 3: Run tests — confirm PASS**

```bash
go test ./internal/mail/... -v -count=1
go test -race ./internal/mail/... -count=1
```

- [ ] **Step 4: Commit**

```bash
git add internal/mail/
git commit -m "feat: add mail package with Notifier interface, Mailer, and MockMailer"
```

---

### Task 2: Submit handler tests (red phase)

**Files:**
- Create: `internal/handler/submit.go` (stub)
- Create: `internal/handler/submit_test.go`

- [ ] **Step 1: Write all 15 submit handler tests**

Each test creates an in-memory store, a MockMailer, and a SubmitHandler, then uses httptest.

```go
package handler

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"

    "github.com/go-chi/chi/v5"
    "github.com/youruser/dsforms/internal/mail"
    "github.com/youruser/dsforms/internal/store"
)

func setupSubmit(t *testing.T) (*store.Store, *mail.MockMailer, *chi.Mux) {
    t.Helper()
    s, err := store.New(":memory:")
    if err != nil {
        t.Fatalf("store.New error: %v", err)
    }
    m := &mail.MockMailer{}
    h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com"}
    r := chi.NewRouter()
    r.Post("/f/{formID}", h.Handle)
    // Create a test form
    _ = s.CreateForm(store.Form{ID: "test-form", Name: "Test", EmailTo: "test@example.com", Redirect: "https://example.com/thanks"})
    return s, m, r
}

func TestSubmitUnknownForm(t *testing.T) {
    t.Parallel()
    _, _, r := setupSubmit(t)
    req := httptest.NewRequest("POST", "/f/nonexistent", strings.NewReader("name=test"))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusNotFound {
        t.Errorf("status = %d, want 404", w.Code)
    }
}

func TestSubmitHoneypotIgnored(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    form := url.Values{"name": {"test"}, "_honeypot": {"bot-value"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    // Should redirect (success) but NOT store submission
    subs, _ := s.ListSubmissions("test-form")
    if len(subs) != 0 {
        t.Errorf("submissions = %d, want 0 (honeypot should be ignored)", len(subs))
    }
}

func TestSubmitValidStoresSubmission(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}, "email": {"alice@example.com"}, "message": {"Hello"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    subs, _ := s.ListSubmissions("test-form")
    if len(subs) != 1 {
        t.Fatalf("submissions = %d, want 1", len(subs))
    }
    if subs[0].Data["name"] != "Alice" {
        t.Errorf("Data[name] = %q, want Alice", subs[0].Data["name"])
    }
}

func TestSubmitTriggersEmail(t *testing.T) {
    t.Parallel()
    _, m, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    // Email is async — give goroutine a moment
    // Actually, for deterministic tests, we should wait. Use a channel or check after small delay.
    // Since MockMailer is synchronous in recording, and the goroutine runs immediately,
    // we can check with a brief spin. But CLAUDE.md says no time.Sleep.
    // Solution: make the handler accept a "sync" flag for testing, OR check immediately
    // since the goroutine will likely have run by the time we check.
    // Simplest: check after the response is written. The goroutine should have started.
    // In practice with httptest, the goroutine runs before we check.
    if m.CallCount() < 1 {
        // Small retry with runtime.Gosched
        for i := 0; i < 100; i++ {
            if m.CallCount() >= 1 {
                break
            }
        }
    }
    if m.CallCount() != 1 {
        t.Errorf("email calls = %d, want 1", m.CallCount())
    }
}

func TestSubmitRedirectOverride(t *testing.T) {
    t.Parallel()
    _, _, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}, "_redirect": {"https://other.com/done"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusFound {
        t.Errorf("status = %d, want 302", w.Code)
    }
    if loc := w.Header().Get("Location"); loc != "https://other.com/done" {
        t.Errorf("Location = %q, want https://other.com/done", loc)
    }
}

func TestSubmitDefaultRedirect(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    // Create form with no redirect
    _ = s.CreateForm(store.Form{ID: "no-redir", Name: "NoRedir", EmailTo: "t@t.com"})
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/no-redir", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if loc := w.Header().Get("Location"); loc != "/success" {
        t.Errorf("Location = %q, want /success", loc)
    }
}

func TestSubmitFormRedirectUsed(t *testing.T) {
    t.Parallel()
    _, _, r := setupSubmit(t)
    // test-form has Redirect: "https://example.com/thanks"
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if loc := w.Header().Get("Location"); loc != "https://example.com/thanks" {
        t.Errorf("Location = %q, want https://example.com/thanks", loc)
    }
}

func TestSubmitJSONResponse(t *testing.T) {
    t.Parallel()
    _, _, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    req.Header.Set("Accept", "application/json")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Errorf("status = %d, want 200", w.Code)
    }
    var resp map[string]bool
    if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
        t.Fatalf("JSON decode error: %v", err)
    }
    if !resp["success"] {
        t.Error("success = false, want true")
    }
}

func TestSubmitEmptyFieldsReturns400(t *testing.T) {
    t.Parallel()
    _, _, r := setupSubmit(t)
    // Only internal fields, no real data
    form := url.Values{"_honeypot": {""}, "_redirect": {"https://x.com"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusBadRequest {
        t.Errorf("status = %d, want 400", w.Code)
    }
}

func TestSubmitXForwardedFor(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    req.Header.Set("X-Forwarded-For", "10.0.0.1, 192.168.1.1")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    subs, _ := s.ListSubmissions("test-form")
    if len(subs) == 0 {
        t.Fatal("no submissions")
    }
    if subs[0].IP != "10.0.0.1" {
        t.Errorf("IP = %q, want 10.0.0.1", subs[0].IP)
    }
}

func TestSubmitXRealIP(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    req.Header.Set("X-Real-IP", "10.0.0.2")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    subs, _ := s.ListSubmissions("test-form")
    if subs[0].IP != "10.0.0.2" {
        t.Errorf("IP = %q, want 10.0.0.2", subs[0].IP)
    }
}

func TestSubmitRemoteAddrFallback(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    // httptest sets RemoteAddr to "192.0.2.1:1234"
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    subs, _ := s.ListSubmissions("test-form")
    if subs[0].IP != "192.0.2.1" {
        t.Errorf("IP = %q, want 192.0.2.1", subs[0].IP)
    }
}

func TestSubmitInternalFieldsNotStored(t *testing.T) {
    t.Parallel()
    s, _, r := setupSubmit(t)
    form := url.Values{
        "name":       {"Alice"},
        "_honeypot":  {""},
        "_redirect":  {"https://x.com"},
        "_subject":   {"Custom Subject"},
    }
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    subs, _ := s.ListSubmissions("test-form")
    if len(subs) == 0 {
        t.Fatal("no submissions")
    }
    for _, key := range []string{"_honeypot", "_redirect", "_subject"} {
        if _, ok := subs[0].Data[key]; ok {
            t.Errorf("internal field %q should not be stored", key)
        }
    }
}
```

**Note on async email test:** Use `MockMailer.Wait()` with a short timeout to deterministically wait for the goroutine. No time.Sleep.

**Note on rate limit + body size tests:** These test the full middleware stack. Create a helper that builds a chi router with rate limiter middleware + MaxBytesReader (already in newRouter from Session 3). Add these 3 tests:

```go
func TestSubmitRateLimitExceeded(t *testing.T) {
    t.Parallel()
    s, _, _ := setupSubmit(t)
    m := mail.NewMockMailer()
    h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com"}
    now := time.Now()
    limiter := ratelimit.NewLimiter(2, 6, func() time.Time { return now })
    r := chi.NewRouter()
    r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", h.Handle)

    for i := 0; i < 2; i++ {
        form := url.Values{"name": {"Alice"}}
        req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        w := httptest.NewRecorder()
        r.ServeHTTP(w, req)
    }
    // 3rd request should be rate limited
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusTooManyRequests {
        t.Errorf("status = %d, want 429", w.Code)
    }
}

func TestSubmitRateLimitJSON(t *testing.T) {
    t.Parallel()
    s, _, _ := setupSubmit(t)
    m := mail.NewMockMailer()
    h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com"}
    now := time.Now()
    limiter := ratelimit.NewLimiter(1, 6, func() time.Time { return now })
    r := chi.NewRouter()
    r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", h.Handle)

    // Exhaust
    form := url.Values{"name": {"Alice"}}
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    // Rate limited with JSON
    req2 := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
    req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    req2.Header.Set("Accept", "application/json")
    w2 := httptest.NewRecorder()
    r.ServeHTTP(w2, req2)
    if w2.Code != http.StatusTooManyRequests {
        t.Errorf("status = %d, want 429", w2.Code)
    }
    var resp map[string]string
    json.NewDecoder(w2.Body).Decode(&resp)
    if resp["error"] != "too many requests" {
        t.Errorf("error = %q, want 'too many requests'", resp["error"])
    }
}

func TestSubmitBodyTooLarge(t *testing.T) {
    t.Parallel()
    s, err := store.New(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    _ = s.CreateForm(store.Form{ID: "test-form", Name: "Test", EmailTo: "t@t.com"})
    m := mail.NewMockMailer()
    h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com"}
    r := chi.NewRouter()
    // Apply MaxBytesReader middleware
    r.Use(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
            next.ServeHTTP(w, r)
        })
    })
    r.Post("/f/{formID}", h.Handle)

    bigBody := strings.Repeat("x", 65*1024)
    req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader("name="+bigBody))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusBadRequest {
        t.Errorf("status = %d, want 400", w.Code)
    }
}
```

**Note:** `rateLimitMiddleware` must be exported from handler package or defined as a test helper. Since main.go also needs it, best to export `ExtractIP` from handler package and keep `rateLimitMiddleware` in main.go but also define it in tests. OR: move rate limit middleware to a shared location. Simplest: define it in the test file as a local helper that mirrors main.go's implementation.

- [ ] **Step 2: Create stub submit.go**

```go
package handler

import "net/http"

// SubmitHandler handles form submissions.
type SubmitHandler struct {
    Store    interface{} // will be *store.Store
    Notifier interface{} // will be mail.Notifier
    BaseURL  string
}

func (h *SubmitHandler) Handle(w http.ResponseWriter, r *http.Request) {
    http.NotFound(w, r)
}
```

- [ ] **Step 3: Run tests — confirm FAIL**

- [ ] **Step 4: Commit**

```bash
git add internal/handler/ internal/mail/
git commit -m "test: add submit handler tests (red)"
```

---

### Task 3: Implement submit handler (green phase)

**Files:**
- Modify: `internal/handler/submit.go`

- [ ] **Step 1: Implement SubmitHandler.Handle**

Follow the 9-step flow from DSFORMS_PLAN.md §6:
1. Get formID from chi.URLParam, look up form — 404 if not found
2. Parse form values: `r.ParseForm()`
3. Honeypot check: if `_honeypot` non-empty → determine redirect and return success silently
4. Filter internal fields (`_honeypot`, `_redirect`, `_subject`), build data map from remaining
5. Validate: data map must have ≥1 key — else 400
6. Determine redirect: `_redirect` > form.Redirect > `/success`
7. Extract IP: X-Forwarded-For > X-Real-IP > RemoteAddr (strip port)
8. Save submission (generate UUID, marshal data to JSON)
9. Send email async: `go h.Notifier.SendNotification(form, sub)` (log errors)
10. Respond: if Accept contains "application/json" → `{"success":true}`, else → 302 redirect

```go
package handler

import (
    "encoding/json"
    "log"
    "net"
    "net/http"
    "strings"

    "github.com/go-chi/chi/v5"
    "github.com/google/uuid"
    "github.com/youruser/dsforms/internal/mail"
    "github.com/youruser/dsforms/internal/store"
)

type SubmitHandler struct {
    Store    *store.Store
    Notifier mail.Notifier
    BaseURL  string
}

func (h *SubmitHandler) Handle(w http.ResponseWriter, r *http.Request) {
    formID := chi.URLParam(r, "formID")
    form, err := h.Store.GetForm(formID)
    if err != nil {
        http.Error(w, "form not found", http.StatusNotFound)
        return
    }

    if err := r.ParseForm(); err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }

    // Honeypot check
    if r.FormValue("_honeypot") != "" {
        redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
        if strings.Contains(r.Header.Get("Accept"), "application/json") {
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]bool{"success": true})
            return
        }
        http.Redirect(w, r, redirectURL, http.StatusFound)
        return
    }

    // Build data map, filtering internal fields
    internalFields := map[string]bool{"_honeypot": true, "_redirect": true, "_subject": true}
    data := make(map[string]string)
    for key, values := range r.PostForm {
        if internalFields[key] {
            continue
        }
        if len(values) > 0 {
            data[key] = values[0]
        }
    }

    // Validate at least 1 real field
    if len(data) == 0 {
        http.Error(w, "no form data", http.StatusBadRequest)
        return
    }

    // Determine redirect
    redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)

    // Extract IP
    ip := extractIP(r)

    // Marshal data to JSON
    rawData, _ := json.Marshal(data)

    // Save submission
    sub := store.Submission{
        ID:      uuid.New().String(),
        FormID:  formID,
        RawData: string(rawData),
        Data:    data,
        IP:      ip,
    }
    if err := h.Store.CreateSubmission(sub); err != nil {
        log.Printf("submit: failed to save submission: %v", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    // Send email async
    go func() {
        if err := h.Notifier.SendNotification(form, sub); err != nil {
            log.Printf("submit: failed to send email: %v", err)
        }
    }()

    // Respond
    if strings.Contains(r.Header.Get("Accept"), "application/json") {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]bool{"success": true})
        return
    }
    http.Redirect(w, r, redirectURL, http.StatusFound)
}

func determineRedirect(formValue, formDefault string) string {
    if formValue != "" {
        return formValue
    }
    if formDefault != "" {
        return formDefault
    }
    return "/success"
}

func extractIP(r *http.Request) string {
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        parts := strings.SplitN(xff, ",", 2)
        return strings.TrimSpace(parts[0])
    }
    if xri := r.Header.Get("X-Real-IP"); xri != "" {
        return xri
    }
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil {
        return r.RemoteAddr
    }
    return host
}
```

- [ ] **Step 2: Run tests — confirm PASS**

```bash
go test ./internal/handler/... -v -count=1
go test -race ./internal/handler/... -count=1
```

- [ ] **Step 3: Commit**

```bash
git add internal/handler/
git commit -m "feat: implement submit handler with honeypot, IP extraction, and async email"
```

---

### Task 4: Wire submit route in main.go

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Update main.go**

Add imports for store, mail, handler, ratelimit, config. Update `main()` to:
1. Open store
2. Create Mailer from config
3. Create SubmitHandler
4. Create Limiter from config
5. Register `POST /f/{formID}` with rate limiter middleware

The `newRouter()` function stays for middleware. The `main()` function does the wiring.

```go
func main() {
    cfg := config.Load()

    s, err := store.New(cfg.DBPath)
    if err != nil {
        log.Fatalf("failed to open database: %v", err)
    }
    defer s.Close()

    mailer := &mail.Mailer{
        Host:    cfg.SMTPHost,
        Port:    cfg.SMTPPort,
        User:    cfg.SMTPUser,
        Pass:    cfg.SMTPPass,
        From:    cfg.SMTPFrom,
        BaseURL: cfg.BaseURL,
    }

    submitHandler := &handler.SubmitHandler{
        Store:    s,
        Notifier: mailer,
        BaseURL:  cfg.BaseURL,
    }

    limiter := ratelimit.NewLimiter(cfg.RateBurst, cfg.RatePerMinute, time.Now)
    limiter.StartCleanup(10*time.Minute, 30*time.Minute)

    r := newRouter()

    // Public routes
    r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", submitHandler.Handle)

    r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        if _, err := w.Write([]byte("ok")); err != nil {
            log.Printf("healthz write error: %v", err)
        }
    })

    log.Printf("starting server on %s", cfg.ListenAddr)
    if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil && err != http.ErrServerClosed {
        log.Fatalf("server error: %v", err)
    }
}

func rateLimitMiddleware(l *ratelimit.Limiter) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ip := extractClientIP(r)
            if !l.Allow(ip) {
                if strings.Contains(r.Header.Get("Accept"), "application/json") {
                    w.Header().Set("Content-Type", "application/json")
                    w.WriteHeader(http.StatusTooManyRequests)
                    json.NewEncoder(w).Encode(map[string]string{"error": "too many requests"})
                    return
                }
                http.Error(w, "Too many requests", http.StatusTooManyRequests)
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}

func extractClientIP(r *http.Request) string {
    // Same logic as handler.extractIP but for middleware use
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        parts := strings.SplitN(xff, ",", 2)
        return strings.TrimSpace(parts[0])
    }
    if xri := r.Header.Get("X-Real-IP"); xri != "" {
        return xri
    }
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil {
        return r.RemoteAddr
    }
    return host
}
```

Move the healthz handler inside `newRouter()` (it's already there). The submit route goes in `main()` because it needs the store/handler instances.

- [ ] **Step 2: Verify build**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: wire submit route with rate limiter middleware in main.go"
```

---

### Task 5: Final verification

- [ ] **Step 1: Full checks**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

All must exit 0.
