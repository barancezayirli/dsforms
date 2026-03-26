# Session 2 — Store (Database Layer) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Full SQLite data layer with all CRUD operations for users, forms, and submissions, tested against in-memory DB.

**Architecture:** Single `internal/store` package with one `Store` struct wrapping `*sql.DB`. All SQL lives here — handlers never call `db.Query` directly. Uses `modernc.org/sqlite` (pure Go, no CGO). Schema migrations run on every startup with `CREATE TABLE IF NOT EXISTS`. Default admin user seeded on first run. bcrypt at cost 12 for all passwords.

**Tech Stack:** Go 1.23, modernc.org/sqlite, golang.org/x/crypto/bcrypt, github.com/google/uuid

---

## Ambiguity Resolution

- **`backup_log` table / `BackupLog` model:** Listed in DSFORMS_SESSIONS.md but has no schema in DSFORMS_PLAN.md. **Deferred to Session 10** per human decision. Tests for `InsertBackupLog`, `UpdateBackupLog`, `ListBackupLogs` are skipped.
- **`is_default_password` column:** Not in the schema SQL in DSFORMS_PLAN.md §4 but described in §4 "Default user seed" section. Must be added to the `users` table schema.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/store/store.go` | Store struct, New(), migrations, seed, all CRUD methods, model types |
| `internal/store/store_test.go` | Table-driven tests for all store methods against `:memory:` DB |

Note: This is a single file because all methods operate on the same `*sql.DB` and share the same transaction patterns. Splitting by entity (users.go, forms.go) would be premature — the file will be ~400 lines which is manageable.

---

## Model Types (from DSFORMS_PLAN.md §4)

```go
type User struct {
    ID                string
    Username          string
    IsDefaultPassword bool
    CreatedAt         time.Time
}

type Form struct {
    ID        string
    Name      string
    EmailTo   string
    Redirect  string
    CreatedAt time.Time
}

type FormSummary struct {
    Form
    UnreadCount int
}

type Submission struct {
    ID        string
    FormID    string
    Data      map[string]string
    RawData   string
    IP        string
    Read      bool
    CreatedAt time.Time
}
```

## Schema (from DSFORMS_PLAN.md §4, with `is_default_password` added)

```sql
CREATE TABLE IF NOT EXISTS users (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    password            TEXT NOT NULL,
    is_default_password INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS forms (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    email_to    TEXT NOT NULL,
    redirect    TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS submissions (
    id          TEXT PRIMARY KEY,
    form_id     TEXT NOT NULL REFERENCES forms(id) ON DELETE CASCADE,
    data        TEXT NOT NULL,
    ip          TEXT NOT NULL DEFAULT '',
    read        INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_submissions_form_id ON submissions(form_id);
CREATE INDEX IF NOT EXISTS idx_submissions_read ON submissions(read);
```

## Store Methods (from DSFORMS_PLAN.md §4)

```
New(path string) (*Store, error)

// Users
GetUserByUsername(username string) (User, error)
GetUserByID(id string) (User, error)
ListUsers() ([]User, error)
CreateUser(username, password string) error
UpdatePassword(userID, newPassword string) error
DeleteUser(id string) error
HasDefaultPassword(userID string) (bool, error)

// Forms
CreateForm(f Form) error
GetForm(id string) (Form, error)
ListForms() ([]FormSummary, error)
UpdateForm(f Form) error
DeleteForm(id string) error

// Submissions
CreateSubmission(sub Submission) error
ListSubmissions(formID string) ([]Submission, error)
MarkRead(submissionID string) error
MarkAllRead(formID string) error
DeleteSubmission(id string) error
UnreadCount(formID string) (int, error)
```

---

### Task 1: Write store tests — initialization and user seeding

**Files:**
- Create: `internal/store/store_test.go`
- Create: `internal/store/store.go` (minimal stub)

- [ ] **Step 1: Write test file with initialization and seeding tests**

Tests to write:
1. `New(":memory:")` succeeds — returns non-nil Store, no error
2. Migrations are idempotent — call `New` twice on same path, no error
3. Default user seeded on first run — username="admin", IsDefaultPassword=true
4. Default user NOT re-seeded on second run — still only one admin

```go
package store

import (
    "testing"

    "golang.org/x/crypto/bcrypt"
)

func mustNew(t *testing.T) *Store {
    t.Helper()
    s, err := New(":memory:")
    if err != nil {
        t.Fatalf("New(:memory:) failed: %v", err)
    }
    return s
}

func TestNew(t *testing.T) {
    t.Parallel()
    s, err := New(":memory:")
    if err != nil {
        t.Fatalf("New(:memory:) error = %v", err)
    }
    if s == nil {
        t.Fatal("New(:memory:) returned nil Store")
    }
}

func TestMigrationsIdempotent(t *testing.T) {
    t.Parallel()
    // Use a temp file so both calls hit the same DB
    dir := t.TempDir()
    path := dir + "/test.db"
    s1, err := New(path)
    if err != nil {
        t.Fatalf("first New() error = %v", err)
    }
    _ = s1
    s2, err := New(path)
    if err != nil {
        t.Fatalf("second New() error = %v", err)
    }
    _ = s2
}

func TestDefaultUserSeeded(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    u, err := s.GetUserByUsername("admin")
    if err != nil {
        t.Fatalf("GetUserByUsername(admin) error = %v", err)
    }
    if u.Username != "admin" {
        t.Errorf("Username = %q, want %q", u.Username, "admin")
    }
    if !u.IsDefaultPassword {
        t.Error("IsDefaultPassword = false, want true")
    }
}

func TestDefaultUserNotReseeded(t *testing.T) {
    t.Parallel()
    dir := t.TempDir()
    path := dir + "/test.db"
    s1, err := New(path)
    if err != nil {
        t.Fatalf("first New() error = %v", err)
    }
    // Change admin password
    u, _ := s1.GetUserByUsername("admin")
    _ = s1.UpdatePassword(u.ID, "newpass")

    // Re-open — should NOT re-seed
    s2, err := New(path)
    if err != nil {
        t.Fatalf("second New() error = %v", err)
    }
    u2, _ := s2.GetUserByUsername("admin")
    // Password should still be the updated one, not re-seeded "admin"
    err = bcrypt.CompareHashAndPassword([]byte(u2.passwordHash), []byte("newpass"))
    if err != nil {
        t.Error("admin password was re-seeded, expected it to remain changed")
    }
}
```

Note: `passwordHash` is an unexported field we'll need for this test since it's in the same package.

- [ ] **Step 2: Create minimal stub store.go**

```go
package store

import (
    "database/sql"
    "time"
)

type Store struct {
    db *sql.DB
}

type User struct {
    ID                string
    Username          string
    IsDefaultPassword bool
    CreatedAt         time.Time
    passwordHash      string // unexported, for internal/test use
}

type Form struct {
    ID        string
    Name      string
    EmailTo   string
    Redirect  string
    CreatedAt time.Time
}

type FormSummary struct {
    Form
    UnreadCount int
}

type Submission struct {
    ID        string
    FormID    string
    Data      map[string]string
    RawData   string
    IP        string
    Read      bool
    CreatedAt time.Time
}

func New(path string) (*Store, error) {
    return nil, nil
}

func (s *Store) GetUserByUsername(username string) (User, error) {
    return User{}, nil
}

func (s *Store) UpdatePassword(userID, newPassword string) error {
    return nil
}
```

- [ ] **Step 3: Run tests — confirm FAIL**

```bash
go test ./internal/store/... -v -count=1
```

Expected: tests fail (nil store, no DB, etc.)

- [ ] **Step 4: Commit**

```bash
git add internal/store/
git commit -m "test: add store initialization and user seeding tests (red)"
```

---

### Task 2: Implement store initialization, migrations, seeding

**Files:**
- Modify: `internal/store/store.go`

- [ ] **Step 1: Implement New(), migrations, default user seed**

Key implementation details:
- Open DB with `modernc.org/sqlite` driver: `sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=ON")`
- Run all `CREATE TABLE IF NOT EXISTS` statements
- Check if users table is empty, seed admin with bcrypt cost 12
- UUID generated via `github.com/google/uuid`
- Log warning when default credentials are active

- [ ] **Step 2: Implement GetUserByUsername and UpdatePassword**

Just enough to make Task 1 tests pass.

- [ ] **Step 3: Run tests — confirm PASS**

```bash
go test ./internal/store/... -v -count=1
```

- [ ] **Step 4: Run race detector**

```bash
go test -race ./internal/store/... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "feat: implement store New() with migrations and default user seed"
```

---

### Task 3: Write and implement user CRUD tests

**Files:**
- Modify: `internal/store/store_test.go`
- Modify: `internal/store/store.go`

- [ ] **Step 1: Write user CRUD tests**

Tests to add:
1. CreateUser bcrypts password (stored hash != plain text)
2. GetUserByUsername returns correct user
3. GetUserByID returns correct user
4. ListUsers returns all users
5. UpdatePassword changes hash, sets IsDefaultPassword=false
6. DeleteUser succeeds for non-last user
7. DeleteUser fails for last remaining user
8. HasDefaultPassword returns true for admin on fresh DB
9. HasDefaultPassword returns false after password update

```go
func TestCreateUserBcryptsPassword(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    err := s.CreateUser("alice", "plaintext")
    if err != nil {
        t.Fatalf("CreateUser error = %v", err)
    }
    u, _ := s.GetUserByUsername("alice")
    if u.passwordHash == "plaintext" {
        t.Error("password stored as plain text, expected bcrypt hash")
    }
}

func TestGetUserByUsername(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    u, err := s.GetUserByUsername("admin")
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if u.Username != "admin" {
        t.Errorf("Username = %q, want %q", u.Username, "admin")
    }
    if u.ID == "" {
        t.Error("ID is empty")
    }
}

func TestGetUserByID(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    u, err := s.GetUserByID(admin.ID)
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if u.Username != "admin" {
        t.Errorf("Username = %q, want %q", u.Username, "admin")
    }
}

func TestListUsers(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateUser("alice", "pass")
    users, err := s.ListUsers()
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if len(users) != 2 {
        t.Errorf("len = %d, want 2", len(users))
    }
}

func TestUpdatePassword(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    err := s.UpdatePassword(admin.ID, "newpass")
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    updated, _ := s.GetUserByUsername("admin")
    if updated.IsDefaultPassword {
        t.Error("IsDefaultPassword = true, want false after update")
    }
}

func TestDeleteUserNonLast(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateUser("alice", "pass")
    alice, _ := s.GetUserByUsername("alice")
    err := s.DeleteUser(alice.ID)
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    _, err = s.GetUserByUsername("alice")
    if err == nil {
        t.Error("expected error for deleted user, got nil")
    }
}

func TestDeleteUserLastFails(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")
    err := s.DeleteUser(admin.ID)
    if err == nil {
        t.Fatal("expected error deleting last user, got nil")
    }
}

func TestHasDefaultPassword(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    admin, _ := s.GetUserByUsername("admin")

    has, err := s.HasDefaultPassword(admin.ID)
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if !has {
        t.Error("HasDefaultPassword = false, want true on fresh DB")
    }

    _ = s.UpdatePassword(admin.ID, "newpass")
    has, _ = s.HasDefaultPassword(admin.ID)
    if has {
        t.Error("HasDefaultPassword = true, want false after password update")
    }
}
```

- [ ] **Step 2: Run tests — confirm new tests FAIL**

```bash
go test ./internal/store/... -v -count=1 -run "TestCreateUser|TestGetUser|TestListUsers|TestUpdatePassword|TestDeleteUser|TestHasDefault"
```

- [ ] **Step 3: Implement all user methods**

Methods: `GetUserByID`, `ListUsers`, `CreateUser`, `DeleteUser`, `HasDefaultPassword` (UpdatePassword and GetUserByUsername already implemented in Task 2).

- [ ] **Step 4: Run tests — confirm PASS**

```bash
go test ./internal/store/... -v -count=1
```

- [ ] **Step 5: Run race detector**

```bash
go test -race ./internal/store/... -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/store/
git commit -m "feat: implement user CRUD methods with bcrypt"
```

---

### Task 4: Write and implement form CRUD tests

**Files:**
- Modify: `internal/store/store_test.go`
- Modify: `internal/store/store.go`

- [ ] **Step 1: Write form CRUD tests**

Tests to add:
1. CreateForm → GetForm round-trip
2. ListForms returns UnreadCount correctly
3. UpdateForm persists changes
4. DeleteForm cascades to submissions

```go
func TestCreateFormGetFormRoundTrip(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    f := Form{
        ID:      "form-1",
        Name:    "Contact",
        EmailTo: "me@example.com",
    }
    if err := s.CreateForm(f); err != nil {
        t.Fatalf("CreateForm error = %v", err)
    }
    got, err := s.GetForm("form-1")
    if err != nil {
        t.Fatalf("GetForm error = %v", err)
    }
    if got.Name != "Contact" {
        t.Errorf("Name = %q, want %q", got.Name, "Contact")
    }
    if got.EmailTo != "me@example.com" {
        t.Errorf("EmailTo = %q, want %q", got.EmailTo, "me@example.com")
    }
}

func TestListFormsWithUnreadCount(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    f := Form{ID: "form-1", Name: "Contact", EmailTo: "me@example.com"}
    _ = s.CreateForm(f)
    _ = s.CreateSubmission(Submission{ID: "sub-1", FormID: "form-1", RawData: `{"name":"Alice"}`})
    _ = s.CreateSubmission(Submission{ID: "sub-2", FormID: "form-1", RawData: `{"name":"Bob"}`})
    _ = s.MarkRead("sub-1")

    forms, err := s.ListForms()
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if len(forms) != 1 {
        t.Fatalf("len = %d, want 1", len(forms))
    }
    if forms[0].UnreadCount != 1 {
        t.Errorf("UnreadCount = %d, want 1", forms[0].UnreadCount)
    }
}

func TestUpdateForm(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    f := Form{ID: "form-1", Name: "Old", EmailTo: "old@example.com"}
    _ = s.CreateForm(f)
    f.Name = "New"
    f.EmailTo = "new@example.com"
    f.Redirect = "https://example.com/thanks"
    if err := s.UpdateForm(f); err != nil {
        t.Fatalf("error = %v", err)
    }
    got, _ := s.GetForm("form-1")
    if got.Name != "New" {
        t.Errorf("Name = %q, want %q", got.Name, "New")
    }
    if got.Redirect != "https://example.com/thanks" {
        t.Errorf("Redirect = %q, want %q", got.Redirect, "https://example.com/thanks")
    }
}

func TestDeleteFormCascades(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    f := Form{ID: "form-1", Name: "Contact", EmailTo: "me@example.com"}
    _ = s.CreateForm(f)
    _ = s.CreateSubmission(Submission{ID: "sub-1", FormID: "form-1", RawData: `{"a":"b"}`})
    if err := s.DeleteForm("form-1"); err != nil {
        t.Fatalf("error = %v", err)
    }
    subs, _ := s.ListSubmissions("form-1")
    if len(subs) != 0 {
        t.Errorf("submissions len = %d, want 0 after cascade delete", len(subs))
    }
}
```

- [ ] **Step 2: Run tests — confirm new tests FAIL**

- [ ] **Step 3: Implement form CRUD methods**

Methods: `CreateForm`, `GetForm`, `ListForms`, `UpdateForm`, `DeleteForm`

- [ ] **Step 4: Also implement stub submission methods** needed by form tests

Methods: `CreateSubmission`, `ListSubmissions`, `MarkRead` (just enough for form tests to work)

- [ ] **Step 5: Run tests — confirm PASS**

- [ ] **Step 6: Commit**

```bash
git add internal/store/
git commit -m "feat: implement form CRUD with cascade delete"
```

---

### Task 5: Write and implement submission CRUD tests

**Files:**
- Modify: `internal/store/store_test.go`
- Modify: `internal/store/store.go`

- [ ] **Step 1: Write submission tests**

Tests to add:
1. CreateSubmission → ListSubmissions round-trip
2. ListSubmissions unmarshals Data from RawData JSON
3. MarkRead sets read=true
4. MarkAllRead marks all for a form
5. DeleteSubmission removes row
6. UnreadCount returns correct count after read/delete

```go
func TestCreateSubmissionListSubmissions(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
    sub := Submission{ID: "s1", FormID: "f1", RawData: `{"name":"Alice","email":"a@b.com"}`, IP: "1.2.3.4"}
    if err := s.CreateSubmission(sub); err != nil {
        t.Fatalf("error = %v", err)
    }
    subs, err := s.ListSubmissions("f1")
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if len(subs) != 1 {
        t.Fatalf("len = %d, want 1", len(subs))
    }
    if subs[0].Data["name"] != "Alice" {
        t.Errorf("Data[name] = %q, want %q", subs[0].Data["name"], "Alice")
    }
    if subs[0].IP != "1.2.3.4" {
        t.Errorf("IP = %q, want %q", subs[0].IP, "1.2.3.4")
    }
    if subs[0].Read {
        t.Error("Read = true, want false for new submission")
    }
}

func TestMarkRead(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
    _ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
    if err := s.MarkRead("s1"); err != nil {
        t.Fatalf("error = %v", err)
    }
    subs, _ := s.ListSubmissions("f1")
    if !subs[0].Read {
        t.Error("Read = false, want true after MarkRead")
    }
}

func TestMarkAllRead(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
    _ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
    _ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
    if err := s.MarkAllRead("f1"); err != nil {
        t.Fatalf("error = %v", err)
    }
    subs, _ := s.ListSubmissions("f1")
    for _, sub := range subs {
        if !sub.Read {
            t.Errorf("submission %s Read = false, want true", sub.ID)
        }
    }
}

func TestDeleteSubmission(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
    _ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
    if err := s.DeleteSubmission("s1"); err != nil {
        t.Fatalf("error = %v", err)
    }
    subs, _ := s.ListSubmissions("f1")
    if len(subs) != 0 {
        t.Errorf("len = %d, want 0", len(subs))
    }
}

func TestUnreadCount(t *testing.T) {
    t.Parallel()
    s := mustNew(t)
    _ = s.CreateForm(Form{ID: "f1", Name: "C", EmailTo: "m@e.com"})
    _ = s.CreateSubmission(Submission{ID: "s1", FormID: "f1", RawData: `{}`})
    _ = s.CreateSubmission(Submission{ID: "s2", FormID: "f1", RawData: `{}`})
    _ = s.CreateSubmission(Submission{ID: "s3", FormID: "f1", RawData: `{}`})

    count, err := s.UnreadCount("f1")
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if count != 3 {
        t.Errorf("UnreadCount = %d, want 3", count)
    }

    _ = s.MarkRead("s1")
    count, _ = s.UnreadCount("f1")
    if count != 2 {
        t.Errorf("UnreadCount after read = %d, want 2", count)
    }

    _ = s.DeleteSubmission("s2")
    count, _ = s.UnreadCount("f1")
    if count != 1 {
        t.Errorf("UnreadCount after delete = %d, want 1", count)
    }
}
```

- [ ] **Step 2: Run tests — confirm new tests FAIL**

- [ ] **Step 3: Complete submission method implementations**

Methods: `MarkAllRead`, `DeleteSubmission`, `UnreadCount` (CreateSubmission, ListSubmissions, MarkRead may already be partially implemented from Task 4)

- [ ] **Step 4: Run ALL tests**

```bash
go test ./internal/store/... -v -count=1
```

- [ ] **Step 5: Run race detector**

```bash
go test -race ./internal/store/... -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/store/
git commit -m "feat: implement submission CRUD with mark read and unread count"
```

---

### Task 6: Final verification

- [ ] **Step 1: Run full test suite**

```bash
go test ./... -race -count=1
go vet ./...
go build ./...
```

All must exit 0.

- [ ] **Step 2: Verify test count**

Expected: ~20+ store tests + 13 existing tests from Session 1 = 33+ total tests.

- [ ] **Step 3: Commit any remaining changes**
