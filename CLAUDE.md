# DSForms — Development Guide

Self-hosted form endpoint for static websites. Go + SQLite + Docker.

---

## Quick Start

```bash
cp .env.example .env        # set SECRET_KEY at minimum
make dev-up                  # app on :8080, Mailpit on :8025
make test                    # go test ./... -race
```

---

## Code Rules

**Go:**
- Module: `github.com/barancezayirli/dsforms`
- `CGO_ENABLED=0` — uses `modernc.org/sqlite` (pure Go, no CGO)
- No ORM — raw `database/sql` queries only
- No global state outside `main.go`
- All errors wrapped: `fmt.Errorf("descriptive context: %w", err)`
- No `panic` except in `config.Load()` for missing required env vars

**Dependencies — only these four:**
```
github.com/go-chi/chi/v5
modernc.org/sqlite
golang.org/x/crypto
github.com/google/uuid
```

**Templates:**
- Embedded via `//go:embed templates/*` in `main.go`
- Parsed once at startup using per-page clone strategy (avoids `{{define "content"}}` conflicts)
- No hardcoded hex colors — use CSS variables from `base.html`
- No JS frameworks — vanilla JS only for copy-to-clipboard and mobile burger menu

**HTTP:**
- All admin-mutating actions: POST only, never GET
- Session cookie: `HttpOnly`, `SameSite=Lax`, `Secure` if BASE_URL is https
- Rate limiter on `POST /f/{formID}` only
- `http.MaxBytesReader` 64KB on all requests (100MB exception for backup import)
- Security headers on all responses
- Email sending in a goroutine — never blocks the HTTP response

**SQL:**
- All DB operations in `internal/store/store.go` — handlers never call `db.Query` directly
- `_pragma=journal_mode(WAL)` and `_pragma=foreign_keys(1)` on DB open
- Schema migrations via `CREATE TABLE IF NOT EXISTS` on every startup
- No raw string concatenation — always use `?` placeholders

---

## TDD Rules

```
1. Write the test file before the implementation file
2. Run go test to confirm the test FAILS before implementing
3. Write the minimum implementation to make the test pass
4. Every exported function must have at least one test
5. Every error path must have a test
6. Use table-driven tests for functions with multiple input cases
7. Use t.Parallel() in every test that does not share mutable state
8. go test -race must pass
9. No time.Sleep in tests — use time injection instead
10. Test files live next to the file they test: foo_test.go beside foo.go
```

---

## Project Structure

```
dsforms/
├── main.go                    # CLI dispatch + server wiring (no business logic)
├── internal/
│   ├── config/                # env var loading → Config struct
│   ├── store/                 # all SQLite operations (users, forms, submissions, sessions)
│   ├── auth/                  # DB-backed session tokens + RequireAuth middleware
│   ├── flash/                 # one-time flash messages via signed cookie
│   ├── mail/                  # SMTP notification (Notifier interface)
│   ├── ratelimit/             # token bucket + login brute-force guard
│   ├── backup/                # export (VACUUM INTO) + import (validate + atomic swap)
│   └── handler/               # HTTP handlers (submit, auth, admin, users, backup)
├── templates/                 # Go html/template files (embedded)
├── Dockerfile                 # multistage, CGO_ENABLED=0, alpine
├── docker-compose.yml         # production
└── docker-compose.dev.yml     # dev (adds Mailpit)
```

**`main.go` contains only:** config load, store open, handler construction, route registration, CLI dispatch, HTTP server start. No business logic.

**Interfaces defined in the consuming package**, not the implementing package:
- `handler.Notifier` (implemented by `mail.Mailer`)
- `auth.SessionStore` (implemented by `store.Store`)

---

## Architecture Decisions

**Sessions:** DB-backed random tokens. SHA-256 hashed in storage. Password change invalidates all sessions. No HMAC — the token is opaque, looked up in SQLite.

**Templates:** Each page template defines `{{define "content"}}`. Since Go's `html/template` uses a shared namespace, we clone `base.html` per page at startup to avoid conflicts. Templates stored as `map[string]*template.Template`.

**Backup import:** Checkpoint WAL → close old DB → remove WAL/SHM files → `os.Rename` new file → `Store.Reopen` (opens new connection first, then closes old).

**Rate limiting:** In-process token bucket with `sync.Mutex`. Time injection via `func() time.Time` for deterministic tests. Cleanup goroutine removes stale entries.

---

## Adding a New Feature

1. If it touches the DB → add methods to `internal/store/store.go` with tests
2. If it's a new page → create template in `templates/`, add to `pageNames` clone list in `main.go`
3. If it's a new handler → add to the appropriate handler file, wire route in `main.go`
4. Always: test first, implement second, `go test -race ./...` before committing

---

## Git Conventions

```
feat: add store package with user and form CRUD
test: add table-driven tests for config loading
fix: handle empty redirect URL in submit handler
chore: add Makefile with test and build targets
refactor: replace HMAC auth with DB-backed sessions
docs: add README with deployment guide
```
