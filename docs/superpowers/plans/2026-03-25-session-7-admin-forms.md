# Session 7 — Admin UI: Dashboard & Forms CRUD Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dashboard listing forms with unread counts and stats, form create/edit/delete with validation, HTML snippet display, and public success page.

**Architecture:** `internal/handler/admin.go` holds an `AdminHandler` struct with Dashboard, NewFormPage, CreateForm, EditFormPage, EditForm, DeleteForm, and Success methods. Templates extend `base.html` using `{{define "content"}}` blocks. The handler reads flash messages and the current user from the request context for every admin page. Forms use POST for all mutations with `onclick="return confirm(...)"` on delete.

**Tech Stack:** Go `html/template`, chi router, existing store/auth/flash packages.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `templates/dashboard.html` | Forms list with stat strip, unread counts, empty state |
| `templates/form_new.html` | Create form: name, email_to, redirect fields |
| `templates/form_edit.html` | Edit form: pre-filled fields + HTML snippet display |
| `templates/success.html` | Public "message sent" page (no auth) |
| `internal/handler/admin.go` | AdminHandler: Dashboard, NewFormPage, CreateForm, EditFormPage, EditForm, DeleteForm, Success |
| `internal/handler/admin_test.go` | 14 tests for all admin handlers |
| `main.go` | Wire admin routes in protected group + public /success route |

---

## Template Data Types

```go
// Used by all admin pages (extends base.html PageData)
type AdminPageData struct {
    Title       string
    Active      string
    CurrentUser store.User
    Flash       *FlashData
    // Page-specific fields below
}

// Dashboard
type DashboardData struct {
    AdminPageData
    Forms       []store.FormSummary
    TotalForms  int
    TotalUnread int
    TotalAll    int
}

// Form new/edit
type FormPageData struct {
    AdminPageData
    Form     store.Form
    BaseURL  string
    Error    string
    IsEdit   bool
}

// Success (public, no auth)
type SuccessData struct {
    Title string
}
```

---

### Task 1: Create templates (dashboard, form_new, form_edit, success)

**Files:**
- Create: `templates/dashboard.html`
- Create: `templates/form_new.html`
- Create: `templates/form_edit.html`
- Create: `templates/success.html`

- [ ] **Step 1: Create dashboard.html**

Extends base.html. Shows stat strip (total forms, unread, all-time) + forms table with unread counts. Empty state when no forms. Uses CSS from DSFORMS_FRONTEND.md §3.5 (page header), §3.9 (stat cards), §3.7 (cards). Forms table has columns: Name, Email To, Unread, Created, Actions (View/Edit/Delete).

- [ ] **Step 2: Create form_new.html**

Extends base.html. Form with name, email_to, redirect URL fields. Error message display. Uses §3.8 (form inputs), §3.6 (buttons).

- [ ] **Step 3: Create form_edit.html**

Extends base.html. Same as form_new but pre-filled + HTML snippet display with copy button. Shows the form's endpoint URL.

- [ ] **Step 4: Create success.html**

Standalone (like login.html — no base.html extension). Centered message: "Your message has been sent." with a back link.

- [ ] **Step 5: Commit**

```bash
git add templates/
git commit -m "feat: add dashboard, form new/edit, and success templates"
```

---

### Task 2: Admin handler tests + implementation (TDD)

**Files:**
- Create: `internal/handler/admin.go`
- Create: `internal/handler/admin_test.go`

- [ ] **Step 1: Write all 14 admin handler tests**

Tests use inline templates (same pattern as auth tests) to avoid fragile paths. Each test creates an in-memory store, seeds data as needed, and uses httptest.

```go
package handler

// Tests to implement:

// TestDashboardReturns200 — GET /admin/forms with auth cookie → 200
// TestDashboardListsForms — creates 2 forms, checks both names appear in body
// TestDashboardUnreadCounts — creates form + submissions, marks some read, checks count in body
// TestDashboardStatStrip — checks total forms/unread/all-time numbers
// TestDashboardEmptyState — no forms → "No forms yet" message
// TestNewFormPage — GET /admin/forms/new → 200
// TestCreateFormValid — POST with name+email_to → 302 redirect, form exists in store
// TestCreateFormEmptyName — POST with empty name → 200 (re-render with error)
// TestCreateFormEmptyEmail — POST with empty email_to → 200 (re-render with error)
// TestEditFormPage — GET /admin/forms/:id/edit → 200, form data in body
// TestEditFormPost — POST with updated data → 302, store has new values
// TestEditFormNotFound — GET /admin/forms/nonexistent/edit → 404
// TestDeleteForm — POST /admin/forms/:id/delete → 302, form gone from store
// TestDeleteFormNotFound — POST /admin/forms/nonexistent/delete → 404
// TestSuccess — GET /success → 200 (no auth required)
```

Key test helper:
```go
func setupAdmin(t *testing.T) (*store.Store, *chi.Mux) {
    t.Helper()
    s, _ := store.New(":memory:")

    // Inline templates for testing
    tmpl := template.Must(template.New("").Parse(`
        {{define "base"}}{{template "content" .}}{{end}}
        {{define "dashboard"}}{{template "base" .}}{{end}}
        {{define "content"}}
        {{range .Forms}}<span class="form-name">{{.Name}}</span> <span class="unread">{{.UnreadCount}}</span>{{end}}
        {{if not .Forms}}<p>No forms yet</p>{{end}}
        <span class="stat-forms">{{.TotalForms}}</span>
        <span class="stat-unread">{{.TotalUnread}}</span>
        <span class="stat-all">{{.TotalAll}}</span>
        {{end}}
    `))
    // Add form_new, form_edit, success templates similarly...

    ah := &AdminHandler{
        Store:     s,
        SecretKey: testSecretKey,
        BaseURL:   "https://example.com",
        Templates: tmpl,
    }

    r := chi.NewRouter()
    r.Group(func(r chi.Router) {
        r.Use(auth.RequireAuth(s, testSecretKey))
        r.Get("/admin/forms", ah.Dashboard)
        r.Get("/admin/forms/new", ah.NewFormPage)
        r.Post("/admin/forms/new", ah.CreateForm)
        r.Get("/admin/forms/{id}/edit", ah.EditFormPage)
        r.Post("/admin/forms/{id}/edit", ah.EditForm)
        r.Post("/admin/forms/{id}/delete", ah.DeleteForm)
    })
    r.Get("/success", ah.Success) // public, no auth

    return s, r
}
```

Each test gets an auth cookie via:
```go
admin, _ := s.GetUserByUsername("admin")
cookie := auth.CreateSessionCookie(admin.ID, testSecretKey, "https://example.com")
req.AddCookie(cookie)
```

- [ ] **Step 2: Create AdminHandler stub**

```go
type AdminHandler struct {
    Store     *store.Store
    SecretKey string
    BaseURL   string
    Templates *template.Template
}
```

All methods return 200 with empty body initially.

- [ ] **Step 3: Run tests — confirm FAIL**

- [ ] **Step 4: Implement all AdminHandler methods**

**Dashboard:**
1. Get current user from context (auth.UserFromContext)
2. Read flash from request (flash.Get)
3. Call store.ListForms() for forms with unread counts
4. Compute stats: total forms, total unread (sum), total submissions (need a new store method or compute from list)
5. Render dashboard template

Note: "total all-time submissions" needs counting. The simplest approach: iterate forms and sum a total. But ListForms returns FormSummary with UnreadCount. We need TotalCount too. **Decision:** Add a store method `TotalSubmissions() (int, error)` OR compute it as `unread + read` per form. Simpler: the dashboard stat for "all time" counts total submissions across all forms. We can call `ListSubmissions` per form but that's N+1. Better: add a `CountSubmissions(formID string) (int, error)` method to store, or adjust `ListForms` to include total count. Simplest for now: add `TotalCount` to `FormSummary` in the ListForms query.

**Actually:** Let's keep it simple. The dashboard test checks stat strip values. We can compute them:
- TotalForms = len(forms)
- TotalUnread = sum of UnreadCount
- TotalAll = needs a query. Add `CountAllSubmissions() (int, error)` to store.

**NewFormPage:** Render form_new template with empty Form.

**CreateForm:**
1. Parse form values (name, email_to, redirect)
2. Validate: name required, email_to required
3. If invalid: re-render with error
4. Generate UUID for form ID
5. Call store.CreateForm
6. Redirect to /admin/forms/{id}/edit (or /admin/forms/{id})

**EditFormPage:**
1. Get form by ID from chi URL param
2. If not found: 404
3. Render form_edit with pre-filled values + HTML snippet

**EditForm:**
1. Get form ID from URL param, parse form values
2. Call store.UpdateForm
3. Redirect to /admin/forms/{id}/edit with flash

**DeleteForm:**
1. Get form ID, call store.DeleteForm
2. If not found: 404
3. Redirect to /admin/forms

**Success:** Render success.html (no auth required, standalone).

- [ ] **Step 5: Add CountAllSubmissions to store if needed**

```go
func (s *Store) CountAllSubmissions() (int, error) {
    var count int
    err := s.db.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&count)
    if err != nil {
        return 0, fmt.Errorf("count all submissions: %w", err)
    }
    return count, nil
}
```

With a test in store_test.go.

- [ ] **Step 6: Run tests — confirm PASS**

```bash
go test ./internal/handler/... -v -count=1
go test -race ./internal/handler/... -count=1
```

- [ ] **Step 7: Commit**

```bash
git add internal/handler/admin.go internal/handler/admin_test.go internal/store/store.go internal/store/store_test.go
git commit -m "feat: add admin handler with dashboard, forms CRUD, and success page"
```

---

### Task 3: Wire admin routes in main.go

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Create AdminHandler and wire routes**

In the existing protected group, replace the placeholder `/admin/forms` route with real admin routes:

```go
adminHandler := &handler.AdminHandler{
    Store:     s,
    SecretKey: cfg.SecretKey,
    BaseURL:   cfg.BaseURL,
    Templates: tmpl,
}

// Public routes (no auth)
r.Get("/success", adminHandler.Success)

// In the protected group:
r.Get("/admin/forms", adminHandler.Dashboard)
r.Get("/admin/forms/new", adminHandler.NewFormPage)
r.Post("/admin/forms/new", adminHandler.CreateForm)
r.Get("/admin/forms/{id}/edit", adminHandler.EditFormPage)
r.Post("/admin/forms/{id}/edit", adminHandler.EditForm)
r.Post("/admin/forms/{id}/delete", adminHandler.DeleteForm)
```

- [ ] **Step 2: Run full test suite**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: wire admin routes for dashboard and forms CRUD"
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
