# Session 10 — Backup (Export/Import) + CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** DB export download via VACUUM INTO, DB import with validation and atomic swap, CLI user/backup commands baked into the same binary.

**Architecture:** `internal/backup/` handles export (VACUUM INTO temp file) and import (validate SQLite + required tables + integrity check, then atomic rename + Store.Reopen). `internal/handler/backup.go` has BackupHandler with Page/Export/Import. CLI dispatch in `main.go` checks `os.Args[1]` for `user` or `backup` subcommands. Store gets a `Reopen(path)` method and `DB()` accessor for backup operations.

**Tech Stack:** Go stdlib (`os`, `io`, `database/sql`), modernc.org/sqlite, existing store/flash packages.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/store/store.go` | Add Reopen(path) + DB() accessor |
| `internal/backup/backup.go` | Export (VACUUM INTO) + Validate + Import (atomic swap) |
| `internal/backup/backup_test.go` | Tests for export/validate/import |
| `internal/handler/backup.go` | BackupHandler: Page, Export, Import |
| `internal/handler/backup_test.go` | Handler tests |
| `templates/backups.html` | Export/import UI |
| `main.go` | CLI dispatch + backup routes + wiring |

---

### Task 1: Store additions — Reopen + DB accessor (TDD)

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

Add:
```go
// DB returns the underlying *sql.DB for backup operations.
func (s *Store) DB() *sql.DB { return s.db }

// Reopen closes the current DB and opens a new one at the given path.
func (s *Store) Reopen(path string) error
```

`Reopen` closes the current `s.db`, opens a new connection at `path` with the same pragmas, re-runs migrations, and replaces `s.db`. This is used after import to swap to the restored DB.

Tests:
- TestReopen: create store at path A, insert data, reopen at path B (fresh DB), verify old data gone
- TestReopenPreservesMigrations: reopen runs migrations, tables exist
- TestDB: accessor returns non-nil *sql.DB

---

### Task 2: Backup package — export + validate + import (TDD)

**Files:**
- Create: `internal/backup/backup.go`
- Create: `internal/backup/backup_test.go`

Functions:
```go
// Export creates a snapshot of the DB to a temp file using VACUUM INTO.
// Returns the path to the temp file. Caller must delete it after use.
func Export(db *sql.DB) (string, error)

// Validate checks that a file is a valid DSForms SQLite database.
// Checks: is SQLite, integrity_check passes, required tables exist.
func Validate(path string) error

// Import validates the uploaded file, then atomically replaces the live DB.
// Closes the store's current connection, renames the file, reopens the store.
func Import(s *store.Store, uploadedPath, dbPath string) error
```

Tests (matching session spec):
```
TestExportCreatesValidFile — export, then open result with sqlite, query works
TestExportContainsTables — exported DB has users, forms, submissions tables
TestExportDoesNotAffectLiveDB — data still queryable after export
TestValidateValidDB — validate a good DB returns nil
TestValidateInvalidFile — validate a text file returns error
TestValidateMissingTables — validate DB missing required tables returns error
TestValidateIntegrityFail — (hard to test, skip or use corrupted file)
TestImportValidFile — import a valid backup, data is queryable via store
TestImportInvalidFile — import a text file returns error
TestImportFileTooLarge — tested at handler level (100MB limit)
```

---

### Task 3: Backup handler + template (TDD)

**Files:**
- Create: `templates/backups.html`
- Create: `internal/handler/backup.go`
- Create: `internal/handler/backup_test.go`

BackupHandler:
```go
type BackupHandler struct {
    Store     *store.Store
    SecretKey string
    BaseURL   string
    DBPath    string
    Templates map[string]*template.Template
}
```

Methods:
- **Page** — renders backups.html with flash messages
- **Export** — calls backup.Export, streams file, sets Content-Type/Disposition headers
- **Import** — parses multipart upload (100MB limit), writes to temp, calls backup.Import, flash success/error

Template shows two sections: Export (download button) and Import (file upload form with warning).

Tests:
```
TestBackupPage — GET /admin/backups → 200
TestBackupExportHeaders — GET /admin/backups/export → Content-Type octet-stream
TestBackupExportValid — downloaded file is valid SQLite
TestBackupImportValid — POST with valid .db → flash success, redirect
TestBackupImportInvalid — POST with text file → flash error, redirect
TestBackupImportTooLarge — POST with >100MB body → 413 error
```

Note: Flash is already wired for password change and user delete (Session 9). The session spec's flash tests for those actions are already covered by existing tests. This session adds flash for backup import success/failure.

---

### Task 4: CLI commands in main.go

**Files:**
- Modify: `main.go`

Add CLI dispatch at the top of `main()`:
```go
if len(os.Args) > 1 {
    switch os.Args[1] {
    case "user":
        runUserCLI(os.Args[2:])
        return
    case "backup":
        runBackupCLI(os.Args[2:])
        return
    }
}
```

**User CLI:**
```
user list                       — print all users
user add <username> <password>  — create user
user set-password <user> <pw>   — update password
user delete <username>          — delete user
```

**Backup CLI:**
```
backup create                   — VACUUM INTO to BACKUP_LOCAL_DIR
```

Each opens the store directly (DB_PATH from env), does the operation, prints result, exits.

Error cases to test:
- `user delete` on last user → exit 1, "cannot delete the last user"
- `user set-password` on nonexistent user → exit 1, "user not found"
- `user add` with existing username → exit 1, "user already exists"
- `backup create` without BACKUP_LOCAL_DIR → exit 1, error message

CLI is tested via `os/exec` in a test file (run the binary as a subprocess), or by testing the underlying functions.

Note: Export function returns temp file path (not streaming to ResponseWriter directly). Handler does the streaming. This is an intentional deviation from session spec for better separation of concerns.

---

### Task 5: Wire backup routes + final verification

**Files:**
- Modify: `main.go` — create BackupHandler, wire 3 routes, add backups.html to template cloning

Routes:
```go
r.Get("/admin/backups", backupHandler.Page)
r.Get("/admin/backups/export", backupHandler.Export)
r.Post("/admin/backups/import", backupHandler.Import)
```

Final:
```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```
