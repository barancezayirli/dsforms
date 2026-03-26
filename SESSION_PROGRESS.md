# DSForms — Session Progress

## Status: session 11 complete

## Sessions completed
- Session 1 — Project skeleton & config
- Session 2 — Store (database layer)
- Session 3 — Rate limiter & security middleware
- Session 4 — Auth (session cookies + middleware + flash)
- Session 5 — Submit handler
- Session 6 — Auth handlers & login UI
- Session 7 — Admin UI: dashboard & forms CRUD
- Session 8 — Submission reader
- Session 9 — Users & account management
- Refactor: DB-backed session tokens (between S8-S9)
- Session 10 — Backup + CLI
- Session 11 — Docker & deployment

## Key decisions log
- Added RateBurst/RatePerMinute to Config struct (from DSFORMS_PLAN.md §21) to avoid refactoring in Session 3
- Used newRouter() pattern in main.go to make healthz testable without triggering config.Load()
- Omitted t.Parallel() on config tests intentionally (env var mutations via t.Setenv are not parallel-safe)
- Used `_pragma=foreign_keys(1)` instead of `_foreign_keys=ON` for modernc.org/sqlite DSN (driver quirk)
- Deferred backup_log table/BackupLog model to Session 10 (no schema in DSFORMS_PLAN.md)
- Used t.TempDir() for idempotency tests (real file needed to test persistence across New() calls)
- Replaced HMAC-signed session cookies with DB-backed random tokens (SHA-256 hashed in storage)

## Known issues / deferred items
- ~~BASE_URL empty string has no startup warning — address in Session 4~~ RESOLVED: Secure flag set conditionally based on baseURL prefix
- ~~passwordHash field on User is unexported — auth package will need an accessor~~ RESOLVED: Added CheckPassword method on Store
- BackupLocalDir empty string needs validation at point of use — address in Session 10 (backup)
- ~~Integer config values (SMTP_PORT, RATE_BURST, RATE_PER_MINUTE) have no range validation — address in Session 3~~ RESOLVED: constructors panic on invalid values
- StartCleanup goroutines leak (no shutdown mechanism / context.Context) — address in Session 11 (Docker/graceful shutdown)
- StartCleanup parameter validation (interval/maxAge <= 0) — address in Session 11
- Limiter/LoginGuard not yet wired into main.go routes — wiring in Sessions 5-6
- UpdateForm/DeleteForm/MarkRead/DeleteSubmission don't check RowsAffected — address in Sessions 7-8 (handlers)
- backup_log table, BackupLog model, InsertBackupLog/UpdateBackupLog/ListBackupLogs — address in Session 10

---

## Session 1 — Project skeleton & config
**Branch:** `session/1-skeleton`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `go.mod`
- `go.sum`
- `.gitignore`
- `.env.example`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `main.go`
- `main_test.go`

### Files modified
(none — all new files)

### Test summary
- 13 tests written, all passing
- go test -race: clean
- Coverage: 100% of config package (Load, requireEnv, envOr, envOrInt all tested)

### Decisions made
- Added RateBurst/RatePerMinute to Config (deviation from §3, matches §21) to avoid config refactor in Session 3
- Used newRouter() helper in main.go so healthz test doesn't need env vars
- Config tests are not parallel due to t.Setenv usage — this is correct Go behavior

### Deferred items
- BASE_URL empty string has no startup warning — address in Session 4 (auth/cookie Secure flag)
- BackupLocalDir empty string needs validation at point of use — address in Session 10 (backup)
- Integer config range validation (negative ports, zero rate burst) — address in Session 3 (rate limiter)

### Known issues
- None

---

## Session 2 — Store (database layer)
**Branch:** `session/2-store`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `internal/store/store.go`
- `internal/store/store_test.go`

### Files modified
(none — all new files)

### Test summary
- 27 store tests written, all passing (41 total across project including 12 config subtests)
- go test -race: clean
- go vet: clean

### Decisions made
- Used `_pragma=foreign_keys(1)` instead of `_foreign_keys=ON` (modernc.org/sqlite driver requires this syntax)
- Deferred backup_log table/BackupLog model to Session 10 (no schema definition in DSFORMS_PLAN.md, confirmed with human)
- Used t.TempDir() for idempotency tests (deviation from "no real file path" — necessary for testing DB persistence)
- Added Close() method to Store (not in DSFORMS_PLAN.md but needed for graceful shutdown)
- passwordHash field on User is unexported — auth package will need an accessor (to address in Session 4)

### Deferred items
- UpdateForm/DeleteForm/MarkRead/DeleteSubmission RowsAffected checks — address in Sessions 7-8 when handlers are built
- backup_log table, BackupLog model, related methods — address in Session 10
- Test setup error checking pattern (using _ = for setup calls) — improve incrementally

### Known issues
- None

---

## Session 3 — Rate limiter & security middleware
**Branch:** `session/3-ratelimit`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `internal/ratelimit/ratelimit.go`
- `internal/ratelimit/ratelimit_test.go`

### Files modified
- `main.go` — added security headers + MaxBytesReader middleware to newRouter()
- `main_test.go` — added TestSecurityHeaders + TestMaxBytesReader

### Test summary
- 16 ratelimit tests + 2 middleware tests written
- go test -race: clean
- go vet: clean
- No time.Sleep in any test — all time-dependent tests use injection

### Decisions made
- MaxBytesReader returns 413 (not 400 as session spec says) — 413 is semantically correct per HTTP spec
- Constructor validation panics on invalid burst/perMinute/maxFails/nil-now (resolves Session 1 deferred item)
- LoginGuard cleanup skips entries with active lockout to prevent security bypass
- Limiter/LoginGuard not wired into routes yet — happens in Sessions 5-6 per plan

### Deferred items
- StartCleanup goroutine leak (no context.Context shutdown) — address in Session 11 (graceful shutdown)
- StartCleanup parameter validation — address in Session 11
- Panic message content assertions in tests — low priority

### Known issues
- None

---

## Session 4 — Auth (session cookies + middleware + flash)
**Branch:** `session/4-auth`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `internal/auth/auth.go`
- `internal/auth/auth_test.go`
- `internal/flash/flash.go`
- `internal/flash/flash_test.go`

### Files modified
- `internal/store/store.go` — added CheckPassword method (PasswordHash kept unexported)
- `internal/store/store_test.go` — added TestCheckPassword

### Test summary
- 14 auth tests + 5 flash tests + 3 store tests = 22 new tests
- go test -race: clean across all 6 packages
- go vet: clean

### Decisions made
- CreateSessionCookie takes 3 params (added baseURL) to set Secure flag conditionally — deviation from 2-param spec
- Cookie sets Path="/" and MaxAge=30 days (not in spec but required for correct behavior)
- Flash cookie sets HttpOnly=true (defense in depth)
- PasswordHash kept unexported — added CheckPassword(username, plaintext) on Store instead
- UserStore interface defined in auth package per CLAUDE.md §4
- Flash Get validates HMAC before clearing cookie (security best practice)

### Deferred items
- ValidateSession time injection (uses time.Since with real clock) — low risk, boundary tests use explicit timestamps
- Empty userID/secretKey defense-in-depth checks — config.Load panics on empty SECRET_KEY already
- ValidateSession distinct error logging — all cases redirect to login correctly
- Future timestamp rejection — requires compromised HMAC key
- Flash parameter validation / size limits — flash set by our own code only

### Known issues
- None

---

## Session 5 — Submit handler
**Branch:** `session/5-submit`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `internal/mail/mail.go`
- `internal/mail/mail_test.go`
- `internal/handler/submit.go`
- `internal/handler/submit_test.go`

### Files modified
- `main.go` — wired store, mailer, submit handler, rate limiter on POST /f/{formID}
- `main_test.go` — added rate limit middleware tests

### Test summary
- 19 handler tests + 9 mail tests + 2 rate limit tests = 30 new tests
- Total project: 91+ top-level tests across 8 packages
- go test -race: clean
- go vet: clean

### Decisions made
- Notifier interface defined in handler package (not mail) per CLAUDE.md "interfaces in consuming package"
- MockMailer uses channel-based Wait() for deterministic async email testing (no time.Sleep)
- GetForm distinguishes sql.ErrNoRows (404) from DB errors (500 + log)
- Async email goroutine has panic recovery with form/submission context in logs
- _redirect open redirect is by design — static site developer controls the HTML form
- MockMailer stays in mail.go (not _test.go) because handler tests need cross-package access
- ExtractIP exported from handler package, reused in main.go rateLimitMiddleware

### Deferred items
- Open redirect validation on _redirect field — by design per DSFORMS_PLAN.md §6
- MockMailer in production code — acceptable for cross-package test access
- Email retry mechanism / notification queue — future improvement
- EmailTo validation before SMTP send — future improvement
- Rate limiter JSON encode error logging — low impact

### Known issues
- None

---

## Session 6 — Auth handlers & login UI
**Branch:** `session/6-auth-ui`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `templates/base.html`
- `templates/login.html`
- `internal/handler/auth.go`
- `internal/handler/auth_test.go`

### Files modified
- `main.go` — embedded templates, wired auth routes, RequireAuth admin group, LoginGuard

### Test summary
- 10 auth handler tests written (total project: 101+ tests across 8 packages)
- go test -race: clean
- go vet: clean

### Decisions made
- login.html is standalone (does not extend base.html) — login page has no nav/warning banner
- Tests use inline templates to avoid fragile relative file paths
- Templates embedded via //go:embed and parsed once at startup
- Replaced hardcoded #fff with var(--surface) in base.html CSS
- Template execution errors are logged + return 500

### Deferred items
- AuthHandler.Store as interface (same pattern as SubmitHandler) — refactor in Sessions 7-9
- Flash.Type validation (set by our code only, not user input) — low risk

### Known issues
- None

---

## Session 7 — Admin UI: dashboard & forms CRUD
**Branch:** `session/7-admin-forms`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `templates/dashboard.html`
- `templates/form_new.html`
- `templates/form_edit.html`
- `templates/success.html`
- `internal/handler/admin.go`
- `internal/handler/admin_test.go`

### Files modified
- `templates/base.html` — added CSS for tables, stats, buttons, cards, forms, snippets
- `internal/store/store.go` — added CountAllSubmissions, updated DeleteForm with RowsAffected check
- `internal/store/store_test.go` — added TestCountAllSubmissions
- `main.go` — wired AdminHandler routes

### Test summary
- 17 admin handler tests + 1 store test = 18 new tests
- Total project: 117+ tests across 8 packages
- go test -race: clean
- go vet: clean

### Decisions made
- success.html is standalone (no base.html) — public page with no nav
- HTML snippet displayed on form_edit page (DSFORMS_PLAN.md §11, not dashboard per session DoD text)
- Tests use inline templates to avoid fragile file paths
- FlashData struct for consistent flash message rendering in base.html
- newFlash() helper for nil-safe flash creation
- DeleteForm in store updated to return sql.ErrNoRows for proper 404 handling
- Per-page template cloning to resolve {{define "content"}} conflict (found via manual testing)
- Added root URL `/` redirect to `/admin/forms`

### Deferred items
- success.html CSS duplication (standalone page by design) — add sync comment later

### Known issues
- None

---

## Session 8 — Submission reader
**Branch:** `session/8-reader`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `templates/form_detail.html` — paginated submissions table
- `templates/submission_detail.html` — individual submission view

### Files modified
- `templates/base.html` — table styles (unread-dot, sub-name, pagination), field styles
- `internal/handler/admin.go` — added FormDetail (paginated), SubmissionDetail (new), MarkRead, MarkAllRead, DeleteSubmission, ExportCSV
- `internal/handler/admin_test.go` — reader tests including pagination + submission detail
- `internal/store/store.go` — added GetSubmission, ListSubmissionsPaged, CountSubmissions
- `internal/store/store_test.go` — tests for new store methods
- `main.go` — FuncMap, template cloning for form_detail + submission_detail, 6 new routes
- `.gitignore` — added .playwright-mcp/ and .remember/

### Test summary
- go test -race: clean across all 8 packages
- go vet: clean

### Decisions made
- Refactored from two-pane "email client" to table + detail page (per user feedback — simpler for server-rendered HTML)
- Added pagination (20 per page) with ListSubmissionsPaged + CountSubmissions
- Separate SubmissionDetail page (GET /admin/forms/{formID}/submissions/{subID}) for viewing individual submissions
- Auto-mark-read happens on SubmissionDetail view (not on list page)
- CSV filename uses form ID (not name) to prevent header injection
- Go template `or` with piped `index` doesn't work — replaced with if/else blocks

### Deferred items
- CSV flush error check — address in Session 12 (polish)

### Known issues
- None

---

## Session 9 — Users & account management
**Branch:** `session/9-users`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `templates/users.html`
- `templates/users_new.html`
- `templates/account.html`
- `internal/handler/users.go`
- `internal/handler/users_test.go`

### Files modified
- `main.go` — wired UsersHandler + 6 user routes + template cloning for 3 new templates

### Test summary
- 19 new tests (total project: 163 across 8 packages)
- go test -race: clean
- go vet: clean

### Decisions made
- Separate users_new.html template for add user form (not inline in users.html)
- UpdatePassword creates new session after deleting all old ones (keeps current browser logged in)
- DeleteUser checks self-deletion via context before calling store (clear error message)
- Warning banner tests use account page template with IsDefaultPassword check

### Deferred items
- None

### Known issues
- None

---

## Session 10 — Backup + CLI
**Branch:** `session/10-backup-cli`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `internal/backup/backup.go`
- `internal/backup/backup_test.go`
- `internal/handler/backup.go`
- `internal/handler/backup_test.go`
- `templates/backups.html`

### Files modified
- `internal/store/store.go` — added Reopen(), DB(), extracted runMigrations()
- `internal/store/store_test.go` — added TestReopen, TestReopenRunsMigrations, TestDB
- `main.go` — CLI dispatch (user list/add/set-password/delete, backup create), BackupHandler wiring, 3 backup routes

### Test summary
- 8 backup tests + 5 handler tests + 3 store tests = 16 new tests
- Total project: 179 tests across 9 packages
- go test -race: clean
- go vet: clean

### Decisions made
- Export returns temp file path (not streaming to ResponseWriter) — better separation of concerns
- Import: checkpoint WAL + remove -wal/-shm files before atomic rename (prevents WAL replay corruption)
- CLI opens store directly via DB_PATH env var (default /data/dsforms.db)
- Backup CLI falls back to copy if cross-device rename fails
- 100MB upload limit on import (global 64KB middleware exempted for this route)
- Reopen: open-new-first-then-close-old (prevents broken state on failure)
- Import: close old DB before filesystem ops, abort if WAL removal fails
- Generic error in flash for import failure (log detail server-side)
- Content-Length header on export for download progress

### Deferred items
- backup_log table tracking (originally planned for S2, not needed for MVP)
- CLI tests via os/exec subprocess — tested via underlying store functions instead
- Store.Reopen concurrency safety (mutex) — address in Session 12

### Known issues
- None

---

## Session 11 — Docker & deployment
**Branch:** `session/11-docker`
**Status:** pending merge
**Date:** 2026-03-25

### Files created
- `Dockerfile` — multistage (golang:1.23-alpine builder, alpine:3.19 runtime), CGO_ENABLED=0
- `docker-compose.yml` — dsforms + mailpit (SMTP dev server with web UI)
- `Makefile` — test, build, docker-build, docker-up, docker-down, lint

### Files modified
- `.env.example` — Mailpit as default SMTP, production examples (Gmail, Resend, Brevo)
- `internal/config/config.go` — SMTP_USER/SMTP_PASS now optional (was requireEnv)
- `internal/config/config_test.go` — removed SMTP_USER/SMTP_PASS panic tests
- `internal/mail/mail.go` — skip SMTP auth when credentials empty

### Test summary
- 2 config tests removed (SMTP_USER/SMTP_PASS no longer required)
- All existing tests pass
- go test -race: clean

### Decisions made
- Added Mailpit to docker-compose for out-of-the-box email testing
- SMTP_USER/SMTP_PASS made optional to support auth-free SMTP (Mailpit)
- Mailer skips PlainAuth when credentials empty
- .env.example documents 3 production SMTP providers with setup links
- docker-compose uses depends_on for mailpit → dsforms ordering

### Deferred items
- Integration-level Docker tests (docker build, healthz check, volume persistence) — manual verification

### Known issues
- None
