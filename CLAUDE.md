# DSForms — Claude Code Instructions

You are building DSForms: a self-hosted, single-binary form endpoint for static
websites. One Go binary. SQLite. Docker Compose. No external services required.

Read this file completely before doing anything else in this repository.

---

## 1. Required Reading — Do This First, Every Session

Before writing a single line of code or planning a single task, read these
documents in order:

```
1. CLAUDE.md                 ← this file
2. DSFORMS_PLAN.md           ← architecture, data models, all routes, all constraints
3. DSFORMS_FRONTEND.md       ← design tokens, component library, every template spec
4. DSFORMS_SESSIONS.md       ← session definitions, TDD test lists, deliverables
5. SESSION_PROGRESS.md       ← what is already built, decisions made, known issues
```

If any of these files is missing, stop and tell the human before proceeding.
Do not guess what they contain.

---

## 2. The Session Flow — Follow This Exactly

Every session follows this 9-step flow without exception.
Do not skip steps. Do not reorder steps.

```
Step 1 — Branch out
  git checkout main && git pull
  git checkout -b session/N-short-name
  (N and short-name from DSFORMS_SESSIONS.md)

Step 2 — Read docs
  Read all 5 required documents listed above.
  Re-read SESSION_PROGRESS.md carefully — it tells you what already exists.
  Do not re-implement anything already merged.

Step 3 — Write session plan
  Use the writing-plan superpower.
  The plan must include:
    - Exact files to be created or modified (full paths)
    - Exact tests to be written (function names, what they assert)
    - Order of operations (tests before implementation, always)
    - Any questions or ambiguities that need human clarification
  Present the plan to the human. Do not proceed until approved.

Step 4 — Get approval
  Wait for explicit human approval ("looks good", "proceed", "approved", etc.)
  If the human requests changes to the plan, revise and re-present.
  Do not interpret silence as approval.

Step 5 — Implement with TDD
  For each deliverable:
    a. Write the test file first
    b. Run: go test ./path/to/package/... → confirm it FAILS
    c. Write the implementation
    d. Run: go test ./path/to/package/... → confirm it PASSES
    e. Run: go test -race ./path/to/package/... → confirm no races
  Use subagent-driven development for independent packages (see §6).
  After all packages done, wire everything in main.go.
  Final check: go test ./... -race && go vet ./... && go build ./...

Step 6 — PR review
  Run the pr-review toolkit on the branch.
  Fix any issues it raises before proceeding.
  Do not skip this step even for small sessions.

Step 7 — Update SESSION_PROGRESS.md
  Add a new section for this session following the template in §9 below.
  Be precise: list every file created/modified, test count, coverage %.
  List any decisions made that deviate from the plan and why.
  List any items explicitly deferred to a later session.

Step 8 — Get approval to merge
  Present the session summary to the human.
  Wait for explicit merge approval.
  Do not self-merge.

Step 9 — Merge and prepare next
  git checkout main
  git merge --no-ff session/N-short-name
  git push origin main
  git branch -d session/N-short-name
  Tell the human: "Session N complete. Ready to start session N+1 when you are."
```

---

## 3. TDD Rules — Non-Negotiable

These rules apply to every line of production code written in this project.

**The only valid order is: test → fail → implement → pass.**
If you find yourself writing implementation before a test, stop.

```
Rule 1: Write the test file before the implementation file.
Rule 2: Run go test to confirm the test FAILS before implementing.
        A test that passes before implementation is a bad test.
Rule 3: Write the minimum implementation to make the test pass.
        Do not add untested code "just in case".
Rule 4: Every exported function must have at least one test.
Rule 5: Every error path must have a test.
Rule 6: Use table-driven tests for any function with multiple input cases.
Rule 7: Use t.Parallel() in every test that does not share mutable state.
Rule 8: go test -race must pass. No exceptions.
Rule 9: No time.Sleep in tests. Use time injection instead.
Rule 10: Test files live next to the file they test: foo_test.go beside foo.go.
```

**Table-driven test pattern to follow:**
```go
func TestFoo(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"valid input", "hello", "HELLO", false},
        {"empty input", "", "", true},
    }
    for _, tt := range tests {
        tt := tt
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            got, err := Foo(tt.input)
            if (err != nil) != tt.wantErr {
                t.Fatalf("Foo() error = %v, wantErr %v", err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("Foo() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

**Handler tests use httptest, never a real server:**
```go
func TestHandler(t *testing.T) {
    t.Parallel()
    store := store.New(":memory:")  // real DB, in-memory
    h := handler.New(store)
    req := httptest.NewRequest("GET", "/admin/forms", nil)
    w := httptest.NewRecorder()
    h.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Errorf("expected 200, got %d", w.Code)
    }
}
```

---

## 4. Code Rules — Hard Constraints

These match DSFORMS_PLAN.md §20. Do not deviate without explicit human approval.

**Go:**
- Module: `github.com/youruser/dsforms`
- Go version: 1.23 minimum
- `CGO_ENABLED=0` everywhere — `modernc.org/sqlite` only, no CGO SQLite
- No ORM — raw `database/sql` queries only
- No global state outside `main.go`
- All errors wrapped: `fmt.Errorf("descriptive context: %w", err)`
- No `panic` except in `config.Load()` for missing required env vars
- Interfaces defined in the package that uses them, not the package that implements them
- `Mailer`, `Store` exposed as interfaces so tests can inject fakes

**Dependencies — only these four, nothing else:**
```
github.com/go-chi/chi/v5
modernc.org/sqlite
golang.org/x/crypto
github.com/google/uuid
```
If you think you need a new dependency, ask the human first.

**Templates:**
- All templates embedded via `//go:embed templates/*` in `main.go`
- Templates parsed once at startup, not per-request
- No template reads from filesystem at runtime
- No hardcoded hex color values — only CSS variables from DSFORMS_FRONTEND.md §1
- No JS frameworks — vanilla JS only, only for: copy-to-clipboard, mobile burger menu

**HTTP:**
- All admin-mutating actions: POST only, never GET
- Session cookie: `HttpOnly: true`, `SameSite: Lax`, `Secure` set if BaseURL is https
- Rate limiter on `POST /f/{formID}` only — not on admin routes
- `http.MaxBytesReader` 64KB limit on all request bodies
- Security headers on all responses (see DSFORMS_PLAN.md §21.5)
- Email sending in a goroutine — never blocks the HTTP response

**SQL:**
- All DB operations in `internal/store/store.go` — handlers never call `db.Query` directly
- Use `_journal_mode=WAL` and `_foreign_keys=ON` when opening DB
- Schema migrations run on every startup with `CREATE TABLE IF NOT EXISTS`
- No raw string concatenation in SQL queries — always use `?` placeholders

---

## 5. File Structure Rules

```
dsforms/
├── CLAUDE.md                  ← this file, never modify during sessions
├── DSFORMS_PLAN.md            ← architecture doc, never modify during sessions
├── DSFORMS_FRONTEND.md        ← frontend doc, update if UI changes
├── DSFORMS_SESSIONS.md        ← session plan, never modify during sessions
├── SESSION_PROGRESS.md        ← updated after every session
├── main.go                    ← wiring only, no business logic
├── go.mod / go.sum
├── Dockerfile
├── docker-compose.yml
├── .env.example
├── Makefile
├── README.md
├── .gitignore
│
├── cmd/dsforms/main.go        ← CLI dispatch only
│
├── internal/
│   ├── config/                ← env var loading
│   ├── store/                 ← all DB operations
│   ├── auth/                  ← session cookies, middleware
│   ├── flash/                 ← one-time flash messages
│   ├── mail/                  ← SMTP notification
│   ├── ratelimit/             ← token bucket + login guard
│   ├── backup/                ← export/import logic
│   └── handler/               ← HTTP handlers (no DB calls directly)
│
├── templates/                 ← Go html/template files, embedded
└── docs/                      ← GitHub Pages landing site
    └── index.html
```

**`main.go` contains only:**
- `config.Load()`
- Opening the store
- Constructing handlers
- Registering routes with chi
- Starting the HTTP server
- CLI dispatch to `runUserCLI` / `runBackupCLI`

**No business logic in `main.go`.** If you are writing an `if` statement in
`main.go` that isn't routing or startup, it belongs in a package.

---

## 6. Subagent-Driven Development

When a session has two or more independent packages to implement, use subagents
to work on them in parallel. Follow these rules:

```
- One subagent per package maximum
- Subagents only touch files inside their assigned package directory
- Subagents never touch main.go, go.mod, or go.sum
- Subagents never import from other packages being built in the same session
  (to avoid circular dependency during parallel work)
- Primary agent wires everything together in main.go after subagents finish
- Primary agent runs go test ./... after wiring to confirm integration
```

**Example — Session 10 (backup + flash + CLI):**
```
Subagent A: internal/backup/ only
Subagent B: internal/flash/  only
Primary:    handler/backup.go, main.go CLI wiring, integration tests
```

---

## 7. Git Rules

```
Branches:    session/N-short-name (e.g. session/2-store)
Commits:     conventional commits format
             feat: add store package with user and form CRUD
             test: add table-driven tests for config loading
             fix: handle empty redirect URL in submit handler
             chore: add Makefile with test and build targets
Merge:       --no-ff always (preserves session history)
No force push to main.
One PR per session.
PR title matches session goal from DSFORMS_SESSIONS.md.
```

**Commit often.** After each green test run, commit. Don't accumulate a session
worth of changes in one commit.

---

## 8. What to Do When Something Is Unclear

In order of preference:

1. Check DSFORMS_PLAN.md — the answer is probably there
2. Check DSFORMS_FRONTEND.md — for anything UI related
3. Check SESSION_PROGRESS.md — for context on prior decisions
4. Ask the human — state the question clearly, present 2-3 options if applicable

Do not make assumptions about ambiguous requirements. A question asked is faster
than a wrong implementation undone.

---

## 9. SESSION_PROGRESS.md Update Template

After every session, append this block to SESSION_PROGRESS.md:

```markdown
---

## Session N — Short name
**Branch:** `session/N-short-name`
**Status:** merged to main
**Date:** YYYY-MM-DD

### Files created
- `path/to/file.go`
- `path/to/file_test.go`

### Files modified
- `main.go` — added route wiring for X

### Test summary
- N tests written, all passing
- go test -race: clean
- Coverage: X% (run: go test ./... -coverprofile=c.out && go tool cover -func=c.out)

### Decisions made
- Any deviation from DSFORMS_PLAN.md and the reason
- Any design choice not explicitly specified in the plan

### Deferred items
- Anything explicitly punted to a later session with the session number it belongs to

### Known issues
- Any failing edge cases or things to revisit
- "none" if clean
```

---

## 10. Definition of Done — Every Session

A session is not done until all of these are true:

```
[ ] go build ./...                     exits 0
[ ] go test ./... -race -count=1       exits 0, zero failures
[ ] go vet ./...                       exits 0, zero warnings
[ ] pr-review toolkit run              issues addressed
[ ] SESSION_PROGRESS.md updated        new session block appended
[ ] Human has approved the merge
[ ] Merged to main with --no-ff
[ ] Branch deleted
```

---

## 11. Definition of Done — Full Project

The project is complete when all sessions are merged and:

```
[ ] docker compose up starts cleanly from a fresh clone
[ ] GET /healthz returns 200
[ ] Full user flow works: login → create form → submit from curl → read in UI
[ ] Default password warning appears and disappears correctly
[ ] Export downloads a valid .db file
[ ] Import restores that .db file cleanly
[ ] docker compose exec dsforms ./dsforms user list works
[ ] go test ./... -race coverage ≥ 80%
[ ] Lighthouse score on docs/index.html ≥ 95 performance
[ ] README renders correctly on GitHub
[ ] No TODO comments remaining in codebase
```

---

## 12. Quick Reference

| Thing | Location |
|---|---|
| All routes | DSFORMS_PLAN.md §10 |
| DB schema | DSFORMS_PLAN.md §4 |
| Config env vars | DSFORMS_PLAN.md §3 |
| CSS design tokens | DSFORMS_FRONTEND.md §1 |
| Component HTML/CSS | DSFORMS_FRONTEND.md §3 |
| Template specs | DSFORMS_FRONTEND.md §4 |
| Session deliverables | DSFORMS_SESSIONS.md |
| Test lists per session | DSFORMS_SESSIONS.md |
| What's already built | SESSION_PROGRESS.md |
| Hard constraints | DSFORMS_PLAN.md §20 |
| Security spec | DSFORMS_PLAN.md §21 |
