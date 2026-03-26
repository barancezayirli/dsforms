# Session 8 — Submission Reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two-pane submission reader with message list + reading pane, mark read/all, delete submission, CSV export — the "email client" for form submissions.

**Architecture:** Add 5 new methods to the existing `AdminHandler` in `internal/handler/admin.go`: `FormDetail`, `MarkRead`, `MarkAllRead`, `DeleteSubmission`, `ExportCSV`. Add `templates/form_detail.html` extending base.html with reader-specific CSS. The FormDetail handler selects an active submission (first unread, or `?sub=ID` param), auto-marks it read, and computes prev/next indices. CSV export collects the union of all field keys across submissions and writes RFC 4180 CSV.

**Tech Stack:** Go `html/template`, `encoding/csv`, chi router, existing store/auth/flash packages.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `templates/form_detail.html` | Two-pane reader layout with message list, reading pane, prev/next nav, empty state |
| `internal/handler/admin.go` | Add FormDetail, MarkRead, MarkAllRead, DeleteSubmission, ExportCSV methods |
| `internal/handler/admin_test.go` | Add 14 tests for reader functionality |
| `templates/base.html` | Add reader-specific CSS (topbar, msg-list, pane, field styles) |
| `main.go` | Wire 5 new routes in protected group |

---

### Task 1: Create form_detail.html template + add reader CSS to base.html

**Files:**
- Create: `templates/form_detail.html`
- Modify: `templates/base.html` — add reader CSS

The template receives:
```go
type formDetailData struct {
    Title       string
    Active      string
    CurrentUser store.User
    Flash       *FlashData
    Form        store.Form
    Submissions []store.Submission
    ActiveSub   store.Submission  // the currently selected submission
    ActiveIdx   int               // 0-based index
    HasActive   bool
    TotalCount  int
    UnreadCount int
    PrevID      string  // empty if no prev
    NextID      string  // empty if no next
    BaseURL     string
}
```

The template shows:
- Topbar with breadcrumb (Forms / Form Name), Mark all read, Export CSV, Edit form buttons
- Left pane: message list with unread indicators (mint border + dot)
- Right pane: active submission fields with field-msg styling for message/body/content keys
- Prev/Next navigation footer
- Empty state when no submissions

**Responsive design:** Include `@media (max-width: 640px)` rules in base.html:
- `.reader` becomes `flex-direction: column`
- `.msg-list` becomes `width: 100%; min-width: auto; max-height: 240px; border-right: none; border-bottom: 1px solid var(--border)`
- Reader height adjusts to `auto` instead of `calc(100vh - ...)`

Commit templates separately first.

---

### Task 2: Reader handler tests + implementation (TDD)

**Files:**
- Modify: `internal/handler/admin.go` — add 5 methods
- Modify: `internal/handler/admin_test.go` — add 14 tests

Tests (matching DSFORMS_SESSIONS.md):

```
TestFormDetailReturns200
TestFormDetailFirstUnreadActive — seeds 3 subs, marks 1 read, checks first unread is active
TestFormDetailAutoMarksRead — active submission gets marked read
TestFormDetailSubParam — ?sub=ID selects specific submission
TestFormDetailPrevNext — checks prev/next IDs are correct
TestFormDetailEmptyState — no submissions → empty state
TestMarkReadPost — POST marks single read, redirect back
TestMarkReadNotFound — non-existent → 404
TestMarkAllReadPost — marks all read, redirect back
TestDeleteSubmissionPost — removes submission, redirect back
TestDeleteSubmissionNotFound — non-existent → 404
TestExportCSVContentType — Content-Type: text/csv
TestExportCSVHeaders — union of all field keys as CSV header
TestExportCSVValues — correct values, empty for missing fields
TestExportCSVEmpty — empty form → header only
```

Key implementation details:

**FormDetail(w, r):**
1. Get form by ID from URL param
2. List submissions for this form
3. Determine active submission: use `?sub=ID` if present, else first unread, else first overall
4. If active submission exists and is unread, auto-mark it read via store.MarkRead
5. Compute prev/next IDs based on active index in the submissions list
6. Compute unread count
7. Render form_detail.html

**MarkRead(w, r):**
- Get submission ID from chi.URLParam
- Call store.MarkRead — 404 if error
- Redirect back to form detail (need form_id — get from the Referer header or from the submission itself)

Note: MarkRead route is `POST /admin/submissions/{id}/read`. We need to know which form to redirect back to. Options:
a) Store the submission, get its FormID, redirect to /admin/forms/{formID}
b) Read Referer header

Simplest: add a `GetSubmission(id) (Submission, error)` method to store to get the formID. OR just redirect based on a hidden form field. Actually, the spec says "redirect back" — use the Referer header as fallback, or add a `form_id` query param.

Decision: Add `GetSubmission(id string) (Submission, error)` to store for this session. It's needed for MarkRead and DeleteSubmission to know which form to redirect to.

**MarkAllRead(w, r):**
- Get form ID from URL param (`POST /admin/forms/{id}/read-all`)
- Call store.MarkAllRead
- Redirect to /admin/forms/{id}

**DeleteSubmission(w, r):**
- Get submission ID from URL param (`POST /admin/submissions/{id}/delete`)
- Get submission first to know form ID for redirect
- Call store.DeleteSubmission — 404 if error
- Redirect to /admin/forms/{formID}

**ExportCSV(w, r):**
1. Get form ID, get form name for filename
2. List submissions
3. Collect union of all data keys across all submissions
4. Sort keys alphabetically
5. Write CSV header: id, submitted_at, ip, read, key1, key2, ...
6. Write rows: sub.ID, sub.CreatedAt, sub.IP, sub.Read, data[key1], data[key2], ...
7. Set Content-Type: text/csv, Content-Disposition: attachment; filename="formname-submissions.csv"

---

### Task 3: Add GetSubmission to store + wire routes in main.go

**Files:**
- Modify: `internal/store/store.go` — add GetSubmission
- Modify: `internal/store/store_test.go` — add test
- Modify: `main.go` — wire 5 new routes

Store method:
```go
func (s *Store) GetSubmission(id string) (Submission, error) {
    var sub Submission
    var rawData string
    var readInt int
    err := s.db.QueryRow(
        "SELECT id, form_id, data, ip, read, created_at FROM submissions WHERE id = ?", id,
    ).Scan(&sub.ID, &sub.FormID, &rawData, &sub.IP, &readInt, &sub.CreatedAt)
    if err != nil {
        return Submission{}, fmt.Errorf("get submission: %w", err)
    }
    sub.RawData = rawData
    sub.Read = readInt == 1
    json.Unmarshal([]byte(rawData), &sub.Data)
    return sub, nil
}
```

Routes to wire in the protected group:
```go
r.Get("/admin/forms/{id}", adminHandler.FormDetail)
r.Post("/admin/forms/{id}/read-all", adminHandler.MarkAllRead)
r.Get("/admin/forms/{id}/export", adminHandler.ExportCSV)
r.Post("/admin/submissions/{id}/read", adminHandler.MarkRead)
r.Post("/admin/submissions/{id}/delete", adminHandler.DeleteSubmission)
```

---

### Task 4: Final verification

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

All must exit 0.
