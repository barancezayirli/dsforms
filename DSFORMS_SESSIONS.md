# DSForms — Implementation Plan (Session by Session)

This document defines every development session from zero to shipped.
Each session maps to one Git branch, one PR, one merge.

---

## How Every Session Works

Claude Code follows this exact flow for every session without exception:

```
1. Branch out       git checkout -b session/N-short-name
2. Read docs        CLAUDE.md → DSFORMS_PLAN.md → DSFORMS_FRONTEND.md → SESSION_PROGRESS.md
3. Write plan       Use writing-plan superpower to produce a session plan
4. Get approval     Present plan, wait for human approval before writing any code
5. Implement        TDD: write failing tests first, then implement until green
                    Use subagent-driven development for parallel workstreams
6. PR review        Run pr-review toolkit on the branch before marking ready
7. Update progress  Update SESSION_PROGRESS.md with what was done, what passed, any decisions
8. Get approval     Present summary, wait for human merge approval
9. Merge & next     Merge to main, branch out for next session
```

**Non-negotiables:**
- No code is written before the session plan is approved
- No PR is merged before the session summary is approved
- Every public function has a test
- `go test ./...` must pass with zero failures before PR is opened
- `go vet ./...` and `go build ./...` must pass clean

---

## Repository Bootstrap (before Session 1)

Human does this once before any session begins:

```bash
# Create repo
mkdir dsforms && cd dsforms
git init
git checkout -b main

# Create CLAUDE.md (tells Claude Code how to behave in this repo)
# Create SESSION_PROGRESS.md (starts empty, updated each session)
# Commit both
git add . && git commit -m "chore: bootstrap repo with claude docs"
```

### `CLAUDE.md` contents

```markdown
# DSForms — Claude Code Instructions

## Required reading before any work
1. DSFORMS_PLAN.md       — architecture, data models, all routes, constraints
2. DSFORMS_FRONTEND.md   — design tokens, component library, all templates
3. SESSION_PROGRESS.md   — what has been built so far, decisions made

## Session flow
Follow the 9-step session flow defined in DSFORMS_SESSIONS.md exactly.
Never skip the plan approval step. Never skip the merge approval step.

## Code rules
- No CGO. modernc.org/sqlite only.
- No ORM. Raw database/sql queries only.
- No JS framework. Vanilla JS only (clipboard copy + burger menu).
- No external CSS. All styles in base.html only.
- No hardcoded hex colors in templates — use CSS variables from DSFORMS_FRONTEND.md §1.
- All tests use t.Parallel() where safe.
- Table-driven tests for anything with multiple input cases.
- Test file naming: foo_test.go lives next to foo.go.
- All errors are wrapped with fmt.Errorf("context: %w", err).
- No global state outside of main.go.

## TDD rules
- Write the test file first, run go test to confirm it fails.
- Implement until go test passes.
- Do not write implementation before the test exists.

## Subagent rules
When using subagents for parallel work:
- Each subagent works on a single package only.
- Subagents do not modify main.go or go.mod.
- Integration wiring in main.go is done by the primary agent after subagents finish.
```

### `SESSION_PROGRESS.md` initial contents

```markdown
# DSForms — Session Progress

## Status: not started

## Sessions completed
(none)

## Key decisions log
(none)

## Known issues / deferred items
(none)
```

---

## Session 1 — Project skeleton & config

**Branch:** `session/1-skeleton`
**Goal:** Repo compiles, config loads, health endpoint responds, CI passes.

### Deliverables
- `go.mod` with module `github.com/youruser/dsforms` and Go 1.23
- `go.sum`
- `internal/config/config.go` — `Load()` reads all env vars, panics on missing required
- `internal/config/config_test.go` — table-driven tests for all env var combinations
- `main.go` — calls `config.Load()`, starts HTTP server, serves `GET /healthz → 200 OK`
- `.env.example`
- `.gitignore`

### Tests to write first
```
config: Load() with all vars set → no panic
config: Load() missing SECRET_KEY → panic
config: Load() missing SMTP_HOST → panic
config: Load() defaults applied (ListenAddr, DBPath, SMTPPort)
config: BACKUP_LOCAL_DIR empty string when not set (no panic)
healthz: GET /healthz returns 200 and body "ok"
```

### Definition of done
- `go build ./...` clean
- `go test ./...` green
- `go vet ./...` clean
- `curl localhost:8080/healthz` returns `ok`

---

## Session 2 — Store (database layer)

**Branch:** `session/2-store`
**Goal:** Full SQLite data layer with all CRUD operations, tested against in-memory DB.

### Deliverables
- `internal/store/store.go`
  - `New(path string) (*Store, error)` — opens DB, runs migrations, seeds default user
  - All schema tables: `users`, `forms`, `submissions`, `backup_log`
  - All model types: `User`, `Form`, `FormSummary`, `Submission`, `BackupLog`
  - All store methods as defined in DSFORMS_PLAN.md §4
- `internal/store/store_test.go`

### Tests to write first
```
store: New(":memory:") succeeds
store: migrations are idempotent (run New twice, no error)
store: default user seeded on first run (username="admin", IsDefaultPassword=true)
store: default user NOT re-seeded on second run (only one admin)
store: CreateForm → GetForm round-trip
store: ListForms returns UnreadCount correctly
store: UpdateForm persists changes
store: DeleteForm cascades to submissions
store: CreateSubmission → ListSubmissions
store: MarkRead sets read=true
store: MarkAllRead marks all for a form
store: DeleteSubmission removes row
store: UnreadCount returns correct count after read/delete
store: CreateUser bcrypts password (stored hash != plain)
store: GetUserByUsername returns correct user
store: ListUsers returns all users
store: UpdatePassword changes hash, sets IsDefaultPassword=false
store: DeleteUser succeeds for non-last user
store: DeleteUser fails for last remaining user
store: HasDefaultPassword returns true for admin on fresh DB
store: InsertBackupLog + UpdateBackupLog + ListBackupLogs
```

### Definition of done
- All tests pass against `:memory:` DB
- No test uses a real file path
- `go test -race ./internal/store/...` clean

---

## Session 3 — Rate limiter & security middleware

**Branch:** `session/3-ratelimit`
**Goal:** In-process token bucket limiter and login guard, security headers, body size limit.

### Deliverables
- `internal/ratelimit/ratelimit.go`
  - `Limiter` — per-IP token bucket
  - `LoginGuard` — failed attempt tracker with lockout
  - Cleanup goroutines for both
- `internal/ratelimit/ratelimit_test.go`

### Tests to write first
```
limiter: first N requests within burst succeed
limiter: request after burst exhausted returns false
limiter: tokens refill over time (use time injection)
limiter: different IPs have independent buckets
limiter: cleanup removes stale entries
loginGuard: first 4 failures do not lock
loginGuard: 5th failure triggers lockout
loginGuard: locked IP returns true from IsLocked
loginGuard: lockout expires after duration
loginGuard: successful login resets counter for IP
loginGuard: cleanup removes stale entries
```

### Notes
- Inject `time.Now` as a function parameter to make time-dependent tests deterministic.
  Do not call `time.Sleep` in tests.
- Security headers middleware: tested via `httptest.NewRecorder` checking response headers.
- MaxBytesReader middleware: tested by sending a body over 64KB and confirming 400 response.

### Definition of done
- `go test -race ./internal/ratelimit/...` clean
- No `time.Sleep` in any test

---

## Session 4 — Auth (session cookies + middleware)

**Branch:** `session/4-auth`
**Goal:** Session cookie creation/validation, bcrypt password check, RequireAuth middleware.

### Deliverables
- `internal/auth/auth.go`
  - `CreateSessionCookie(userID, secretKey string) *http.Cookie`
  - `ValidateSession(r *http.Request, secretKey string) (userID string, ok bool)`
  - `ClearSessionCookie() *http.Cookie`
  - `RequireAuth(store, secretKey) func(http.Handler) http.Handler`
- `internal/auth/auth_test.go`
- `internal/flash/flash.go`
  - `Set(w, secretKey, msgType, message string)`
  - `Get(r, w, secretKey string) (msgType, message string)`
- `internal/flash/flash_test.go`

### Tests to write first
```
auth: CreateSessionCookie sets HttpOnly=true, SameSite=Lax
auth: CreateSessionCookie value is parseable and contains userID
auth: ValidateSession returns userID for valid cookie
auth: ValidateSession returns ok=false for tampered cookie
auth: ValidateSession returns ok=false for expired cookie (>30 days)
auth: ValidateSession returns ok=false for missing cookie
auth: ClearSessionCookie sets MaxAge=-1
auth: RequireAuth allows request with valid session
auth: RequireAuth redirects to /admin/login with invalid session
auth: RequireAuth loads User into request context
flash: Set writes a signed cookie
flash: Get reads and clears the cookie
flash: Get returns empty strings for missing cookie
flash: Get returns empty strings for tampered cookie
flash: msgType "success" and "error" both round-trip
```

### Definition of done
- `go test -race ./internal/auth/... ./internal/flash/...` clean
- No cookie value hardcoded in tests — always derive from `CreateSessionCookie`

---

## Session 5 — Submit handler

**Branch:** `session/5-submit`
**Goal:** `POST /f/:formID` fully working — honeypot, rate limiting, email dispatch, redirect.

### Deliverables
- `internal/mail/mail.go`
  - `Mailer` struct with `SendNotification(form, submission) error`
  - `MockMailer` for tests (implements same interface)
- `internal/mail/mail_test.go`
- `internal/handler/submit.go`
  - Full submit handler with all steps from DSFORMS_PLAN.md §6
- `internal/handler/submit_test.go`
- Wire route in `main.go`: `POST /f/{formID}`
- Wire rate limiter + MaxBytesReader + security headers middleware

### Tests to write first
```
submit: unknown form ID returns 404
submit: honeypot field non-empty → 200/redirect, no submission stored
submit: valid POST stores submission in DB
submit: valid POST triggers email (mock mailer called once)
submit: _redirect field overrides form redirect
submit: no _redirect and no form redirect → /success
submit: Accept: application/json → returns {"success":true}
submit: missing all non-internal fields → 400
submit: X-Forwarded-For IP extracted correctly
submit: X-Real-IP fallback
submit: RemoteAddr fallback
submit: rate limit exceeded → 429
submit: rate limit exceeded with JSON Accept → {"error":"too many requests"}
submit: body over 64KB → 400
submit: _honeypot, _redirect, _subject NOT stored in submission data
```

### Notes
- Define `Mailer` as an interface so tests inject a mock.
- SMTP implementation is tested separately in `mail_test.go` using a
  local test SMTP server (use `net.Listen` on a random port, speak SMTP manually).
- Do not use external mock libraries.

### Definition of done
- `go test -race ./internal/handler/... ./internal/mail/...` clean
- `curl -X POST http://localhost:8080/f/nonexistent` returns 404
- `curl -X POST http://localhost:8080/f/<valid-id> -d "name=test&email=t@t.com&message=hi"` returns redirect

---

## Session 6 — Auth handlers & login UI

**Branch:** `session/6-auth-ui`
**Goal:** Login page, login POST, logout, RequireAuth wired on all admin routes.

### Deliverables
- `internal/handler/auth.go`
  - `LoginPage` — renders login.html, passes `LoginError bool`
  - `LoginSubmit` — verifies credentials, sets cookie, redirects
  - `Logout` — clears cookie, redirects
- `internal/handler/auth_test.go`
- `templates/login.html` — per DSFORMS_FRONTEND.md §4.2
- `templates/base.html` — full shell with all CSS tokens, nav, warn banner, flash
- Wire routes + RequireAuth in `main.go`

### Tests to write first
```
loginPage: GET /admin/login returns 200
loginPage: GET /admin/login?error=1 renders error message
loginSubmit: valid credentials → 302 to /admin/forms, cookie set
loginSubmit: wrong password → 302 to /admin/login?error=1, no cookie
loginSubmit: wrong username → 302 to /admin/login?error=1, no cookie
loginSubmit: 5th wrong attempt → 429, lockout message
logout: POST /admin/logout clears cookie, redirects to /admin/login
adminGuard: GET /admin/forms without cookie → 302 to /admin/login
adminGuard: GET /admin/forms with valid cookie → 200
adminGuard: GET /admin/forms with tampered cookie → 302 to /admin/login
```

### Notes
- Handler tests use `httptest.NewServer` with a real Store backed by `:memory:` DB.
- Templates are tested via response body string contains checks — not snapshot tests.
- The warning banner appears in the body when `IsDefaultPassword=true`.
- The warning banner is absent after password is changed.

### Definition of done
- `go test -race ./internal/handler/...` clean
- Manual: browser login with `admin`/`admin` works end-to-end
- Manual: wrong password shows error message

---

## Session 7 — Admin UI: dashboard & forms CRUD

**Branch:** `session/7-admin-forms`
**Goal:** Dashboard, form create/edit/delete, HTML snippet display.

### Deliverables
- `internal/handler/admin.go`
  - `Dashboard` — lists forms with unread counts + stats
  - `NewFormPage`, `CreateForm`
  - `EditFormPage`, `EditForm`
  - `DeleteForm`
  - `Success` (public, no auth)
- `internal/handler/admin_test.go`
- `templates/dashboard.html` — per DSFORMS_FRONTEND.md §4.3
- `templates/form_new.html` — per DSFORMS_FRONTEND.md §4.4
- `templates/form_edit.html` — per DSFORMS_FRONTEND.md §4.4
- `templates/success.html` — per DSFORMS_FRONTEND.md §4.8

### Tests to write first
```
dashboard: GET /admin/forms returns 200, lists forms
dashboard: unread counts shown correctly per form
dashboard: stat strip shows correct totals
dashboard: empty state shown when no forms
createForm: GET /admin/forms/new returns 200
createForm: POST with valid data → creates form, redirects to /admin/forms/:id
createForm: POST with empty name → re-renders with error
createForm: POST with empty email_to → re-renders with error
editForm: GET /admin/forms/:id/edit returns 200, pre-fills values
editForm: POST with valid data → updates form, redirects
editForm: GET non-existent ID → 404
deleteForm: POST /admin/forms/:id/delete → removes form + submissions, redirects
deleteForm: non-existent ID → 404
success: GET /success returns 200, no auth required
```

### Definition of done
- `go test -race ./internal/handler/...` clean
- Manual: full form CRUD flow works in browser
- HTML snippet visible on dashboard with correct endpoint URL

---

## Session 8 — Submission reader

**Branch:** `session/8-reader`
**Goal:** The email reader — two-pane submission view, mark read, delete, CSV export.

### Deliverables
- `internal/handler/admin.go` additions:
  - `FormDetail` — renders reader with submissions list + active pane
  - `MarkRead`, `MarkAllRead`
  - `DeleteSubmission`
  - `ExportCSV`
- `templates/form_detail.html` — per DSFORMS_FRONTEND.md §4.5
  - Two-pane layout: message list left, reading pane right
  - Unread indicator (mint left border + dot)
  - Full message display with labeled fields
  - `field-msg` class for message field
  - Prev/Next navigation
  - Empty state

### Tests to write first
```
formDetail: GET /admin/forms/:id returns 200
formDetail: first unread submission shown as active by default
formDetail: active submission is auto-marked read
formDetail: ?sub=ID query param selects specific submission
formDetail: prev/next index calculated correctly
formDetail: empty state rendered when no submissions
markRead: POST marks single submission read
markRead: non-existent submission → 404
markAllRead: POST marks all as read, redirect back
deleteSubmission: POST removes submission, redirect back
deleteSubmission: non-existent → 404
exportCSV: GET returns Content-Type text/csv
exportCSV: CSV header row contains all field names (union across submissions)
exportCSV: CSV rows contain correct values, empty string for missing fields
exportCSV: empty form → CSV with header only
```

### Notes
- `?sub=ID` query param allows direct linking to a specific submission.
- When no `?sub=` param: default to first unread, then first overall.
- Message field detection: if a key is `message` or `body` or `content`, use `.field-msg` styling.
  Otherwise use `.field-val`.

### Definition of done
- `go test -race ./internal/handler/...` clean
- Manual: reader shows full message text without truncation
- Manual: CSV export opens correctly in spreadsheet

---

## Session 9 — Users & account management

**Branch:** `session/9-users`
**Goal:** User CRUD, password change, warning banner wired up.

### Deliverables
- `internal/handler/users.go`
  - `ListUsers`
  - `NewUserPage`, `CreateUser`
  - `DeleteUser`
  - `AccountPage`, `UpdatePassword`
- `internal/handler/users_test.go`
- `templates/users.html` — per DSFORMS_FRONTEND.md §4.6
- `templates/account.html` — per DSFORMS_FRONTEND.md §4.6

### Tests to write first
```
listUsers: GET /admin/users returns 200, lists users
listUsers: current user shown with "you" tag
createUser: POST with valid data → creates user, redirects
createUser: POST with duplicate username → re-renders with error
createUser: POST with mismatched passwords → re-renders with error
createUser: POST with empty username → error
deleteUser: POST /admin/users/:id/delete removes user
deleteUser: cannot delete self → error
deleteUser: cannot delete last user → error
deleteUser: non-existent → 404
accountPage: GET /admin/account returns 200
updatePassword: POST with correct current + matching new → updates, flash success
updatePassword: POST with wrong current password → re-renders with error
updatePassword: POST with mismatched new passwords → error
updatePassword: IsDefaultPassword set to false after update
warnBanner: visible on all admin pages when IsDefaultPassword=true
warnBanner: absent after password updated
```

### Definition of done
- `go test -race ./internal/handler/...` clean
- Manual: warning banner appears after login, disappears after password change
- Manual: cannot delete own account

---

## Session 10 — Backup (export/import) + Flash + CLI

**Branch:** `session/10-backup-cli`
**Goal:** DB export download, DB import with validation, flash messages wired everywhere, CLI commands.

### Deliverables
- `internal/backup/backup.go`
  - `Export(db *sql.DB, w http.ResponseWriter) error`
  - `Import(db *sql.DB, path string) error` (validates, atomically swaps)
  - `Store.Reopen(path string) error`
- `internal/backup/backup_test.go`
- `internal/handler/backup.go`
  - `Page`, `Export`, `Import`
- `internal/handler/backup_test.go`
- `templates/backups.html` — per DSFORMS_FRONTEND.md §4.7
- Flash wired into: password change, user delete, user create, backup import
- CLI in `main.go`: `backup create`, `user list`, `user add`, `user set-password`, `user delete`

### Tests to write first
```
backup export: creates a valid SQLite file
backup export: file contains users, forms, submissions tables
backup export: response has correct Content-Type and Content-Disposition headers
backup export: does not affect live DB
backup import: valid .db file accepted
backup import: invalid file (not SQLite) → error
backup import: missing required tables → error
backup import: integrity_check fail → error
backup import: successful import reopens DB cleanly
backup import: data from uploaded file is queryable after import
backup import: file over 100MB → 413 error
flash: set on password update → visible on next request → gone after
flash: set on user delete → visible on redirect page
flash: set on backup import success
flash: set on backup import failure
cli: user list prints all users
cli: user add creates user, bcrypts password
cli: user set-password updates hash, clears IsDefaultPassword
cli: user delete removes user
cli: user delete last user → exit 1
cli: backup create with BACKUP_LOCAL_DIR set → creates .db file
cli: backup create without BACKUP_LOCAL_DIR → error message
```

### Definition of done
- `go test -race ./internal/backup/... ./internal/handler/...` clean
- Manual: export downloads a valid `.db` file
- Manual: import that file → app works normally
- Manual: import a `.txt` file → flash error, nothing broken
- Manual: `docker compose exec dsforms ./dsforms user list` works

---

## Session 11 — Docker & deployment

**Branch:** `session/11-docker`
**Goal:** Single binary Dockerfile, docker-compose.yml, verified end-to-end.

### Deliverables
- `Dockerfile` — multistage, CGO_ENABLED=0, final image alpine
- `docker-compose.yml` — single service, volume for `/data`, env_file
- `.env.example` — complete with all vars documented
- `Makefile` — convenience targets: `build`, `test`, `docker-build`, `docker-up`, `docker-down`

### Tests (integration level)
These are shell-script level checks, not Go unit tests:
```
docker build completes without error
docker compose up starts the container
GET http://localhost:8080/healthz returns 200
GET http://localhost:8080/admin/login returns 200 (redirect from /admin)
POST /f/<id> from outside the container works
docker compose exec dsforms ./dsforms user list prints admin
docker compose exec dsforms ./dsforms user set-password admin newpass works
volume: data persists across container restart
binary size: ./dsforms is under 30MB
```

### Makefile targets
```makefile
test:
	go test ./... -race -count=1

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dsforms .

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

lint:
	go vet ./...
```

### Definition of done
- `docker compose up` starts cleanly from a fresh clone
- All integration checks pass
- `docker compose exec dsforms ./dsforms` shows usage

---

## Session 12 — Polish, CSV export, README

**Branch:** `session/12-polish`
**Goal:** Tighten everything up, finish CSV export, write README, confirm all responsive breakpoints.

### Deliverables
- `README.md` — all sections per DSFORMS_PLAN.md §18
- Viewport meta tag in `base.html` confirmed
- All responsive breakpoints manually verified at 375px, 640px, 1024px
- All `onclick="return confirm(...)"` dialogs on destructive actions
- Error pages: 404.html, 500.html (basic, styled with design tokens)
- Any TODO comments cleaned up
- `go test ./... -race -count=1` fully green
- `go vet ./...` clean

### Tests to write first
```
404 handler: unknown route returns 404 with styled page
500 handler: panicking handler recovered, returns 500
confirm dialogs: delete form POST without JS confirm → still works server-side
  (confirm is UX only, server always requires POST, never depends on confirm)
```

### Definition of done
- Zero `// TODO` comments in codebase
- `go test ./... -race -count=1 -coverprofile=coverage.out` coverage ≥ 80%
- README renders correctly on GitHub
- App usable on iPhone SE (375px width)

---

## Session 13 — Landing page (GitHub Pages)

**Branch:** `session/13-landing`
**Goal:** `docs/index.html` — single file, no build step, GitHub Pages ready.

### Deliverables
- `docs/index.html` — per DSFORMS_PLAN.md §22
  - Nav with backdrop blur
  - Hero with tagline
  - How it works (3 steps)
  - HTML snippet with manual syntax highlighting + copy button
  - Features checklist
  - Self-host in 5 minutes (terminal-style steps)
  - Footer
- GitHub Pages configured to serve from `/docs` on `main`

### Tests
- Open `docs/index.html` directly in browser (`file://`) — no server needed
- All anchor links resolve
- Copy button works
- Renders correctly at 375px, 768px, 1280px
- Lighthouse score ≥ 95 performance, 100 accessibility
- No console errors
- No external resources except Google Fonts (DM Mono + Inter)

### Definition of done
- File opens from `file://` with no errors
- Live GitHub Pages URL loads correctly
- Lighthouse scores confirmed

---

## Session Progress Tracking

After each session, Claude Code updates `SESSION_PROGRESS.md` with:

```markdown
## Session N — Short name
**Branch:** session/N-short-name
**Merged:** yes/no
**Date:** YYYY-MM-DD

### What was built
- bullet list of files created/modified

### Tests written
- N tests, all passing
- Coverage: X%

### Key decisions made
- any deviations from the plan and why

### Deferred items
- anything explicitly punted to a later session

### Known issues
- any failing edge cases, things to revisit
```

---

## Full Session Map

| # | Branch | Goal | Key files |
|---|---|---|---|
| 1 | `session/1-skeleton` | Config + healthz | `config.go`, `main.go` |
| 2 | `session/2-store` | Database layer | `store.go` |
| 3 | `session/3-ratelimit` | Rate limiting + security | `ratelimit.go` |
| 4 | `session/4-auth` | Session cookies + flash | `auth.go`, `flash.go` |
| 5 | `session/5-submit` | Form submission endpoint | `submit.go`, `mail.go` |
| 6 | `session/6-auth-ui` | Login UI + base template | `auth handler`, `login.html`, `base.html` |
| 7 | `session/7-admin-forms` | Dashboard + forms CRUD | `admin.go`, `dashboard.html` |
| 8 | `session/8-reader` | Email reader UI | `form_detail.html` |
| 9 | `session/9-users` | User management | `users.go`, `users.html` |
| 10 | `session/10-backup-cli` | Backup + flash + CLI | `backup.go`, `backups.html` |
| 11 | `session/11-docker` | Docker + compose | `Dockerfile`, `docker-compose.yml` |
| 12 | `session/12-polish` | Polish + README | `README.md`, error pages |
| 13 | `session/13-landing` | Landing page | `docs/index.html` |

**Estimated sessions:** 13
**Estimated session length:** 45–90 minutes each depending on complexity.
Sessions 2, 5, 8 are the heaviest. Sessions 1, 3, 11, 13 are the lightest.
