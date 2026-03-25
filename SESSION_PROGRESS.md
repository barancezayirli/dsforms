# DSForms — Session Progress

## Status: session 6 complete

## Sessions completed
- Session 1 — Project skeleton & config
- Session 2 — Store (database layer)
- Session 3 — Rate limiter & security middleware
- Session 4 — Auth (session cookies + middleware + flash)
- Session 5 — Submit handler
- Session 6 — Auth handlers & login UI
- Session 4 — Auth (session cookies + middleware + flash)

## Key decisions log
- Added RateBurst/RatePerMinute to Config struct (from DSFORMS_PLAN.md §21) to avoid refactoring in Session 3
- Used newRouter() pattern in main.go to make healthz testable without triggering config.Load()
- Omitted t.Parallel() on config tests intentionally (env var mutations via t.Setenv are not parallel-safe)
- Used `_pragma=foreign_keys(1)` instead of `_foreign_keys=ON` for modernc.org/sqlite DSN (driver quirk)
- Deferred backup_log table/BackupLog model to Session 10 (no schema in DSFORMS_PLAN.md)
- Used t.TempDir() for idempotency tests (real file needed to test persistence across New() calls)

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
