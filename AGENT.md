# dsforms — Agent Guide

Self-hosted form endpoint for static websites. Go + SQLite + Docker, one binary,
no runtime dependencies.

This is the working agreement for anyone — human or agent — changing this repo.
`CLAUDE.md` points here; keep the detail in this file only, because two copies of
a rule is how one of them ends up wrong.

Most of what follows is a rule with a scar behind it. Where a section explains
*why*, that is a bug this codebase actually shipped, not a hypothetical.

---

## 1. Quick start

```bash
cp .env.example .env        # set SECRET_KEY at minimum
make dev-up                 # app on :8080, Mailpit on :8025
make test                   # go test ./... -race
```

Default login is `admin` / `admin`; the admin warns until it is changed.

---

## 2. Development workflow

Follow this for any change beyond a typo. Steps 3 and 7 are the ones that get
skipped under time pressure, and the ones that cost most when skipped.

**1 — Branch.** From up-to-date `main`, using the prefixes in §9. Never commit to
`main` directly.

**2 — Orient.** Read this file and `SESSION_PROGRESS.md`; check
`docs/design/specs/` for a spec covering the area. Do not re-implement something
already merged.

**3 — Plan, and get it approved.** For anything feature-sized, enter plan mode,
research the real code before proposing, and get explicit approval before
editing. **A design document or a handoff is not approval to start implementing**
— it settles *what*, not *how*. Silence is not approval.

**4 — Implement test-first.** Per §7. Work in dependency order: leaf packages,
then `store`, then `handler`, with `main.go` wiring last.

**5 — Verify for real.** `go test -race ./... && go vet ./... && gofmt -l .`,
then *run the thing*. Bugs this suite has been green through: a CSS comment that
silently swallowed the webfont, a timestamp format SQLite could not parse, a
drawer needing five presses of Back to close, and a login page injected into an
overlay. Thirty seconds in a browser caught all four.

**6 — Review your own diff** top to bottom, as a stranger, before asking anyone
else to.

**7 — Run the PR review.** `/pr-review-toolkit:review-pr` on the branch. **Do not
skip this, including for small changes.** On this repo it once returned ~50
findings on a branch whose tests were entirely green — one of them a full spam
bypass. Fix what it finds, or record the finding in `SESSION_PROGRESS.md` with a
reason for deferring. Never silently drop one.

**8 — Update `SESSION_PROGRESS.md`:** what landed, what was deferred and why,
what is still open.

**9 — Propose the merge; do not self-merge.**

---

## 3. Architecture

### Layers and dependency direction

Dependencies point one way. Nothing below imports anything above it, and there
are no cycles.

```
main.go            config, store, handler construction, routes, CLI — no logic
  └── handler      HTTP: request → store/domain calls → template
        ├── auth · backup · broadcaster · mail · webhook   (services → store)
        └── store  every SQL statement in the project
              └── filter · spam · ratelimit · flash        (leaves)
```

`config` also imports `spam`, for `spam.DefaultThreshold`.

**Rules that follow:**

- **Leaves stay leaves.** `spam`, `filter`, `ratelimit` and `flash` import
  nothing from `internal/`. That is what makes them testable without a database.
  `store` imports `filter` and `spam` — downward, and fine.
- **All SQL lives in `internal/store`.** Handlers never touch `db.Query`. A
  handler that needs data needs a store method.
- **Interfaces are declared by the consumer**, never by the implementer.
  `handler` declares one per service it calls (mail, webhooks, digests,
  broadcasts); `auth` declares `SessionStore`; `broadcaster` declares its own
  `Store` and `Mailer`. Satisfaction is checked by the compiler where `main.go`
  wires the concrete type in — `broadcaster` additionally pins its with
  `var _ Store = (*store.Store)(nil)`, which is the better habit.
- **`main.go` holds no business logic**, and there is no global state outside it.

### Adding a package

Check first whether the logic belongs in an existing leaf. A package earns its
place when it has its own vocabulary and is testable alone — `internal/filter`
qualified (validation and matching with no database); one helper function would
not.

### Modularity: check before you write

Before writing a new function, grep for the same *shape*. If it exists, use it.
If it exists twice, consolidate before adding a third.

This repo keeps relearning it. A JSON-decode-with-fallback was inlined at every
call site that needed it; a session-cookie helper was copied into each handler
test; `Title/Active/CurrentUser/Flash` was restated in every page struct until
`Base` absorbed it; and `NavCounts` existed twice — once in `store`, once in
`handler` — with a hand-written field-by-field copy between them that would
silently drop any field added later. That last one was committed one file away
from a comment arguing against exactly it.

The signal is duplicated *shape*, not duplicated text.

---

## 4. Go rules

**Dependencies — only these four.** They are the direct block in `go.mod`;
everything else there is transitive. Adding a fifth is a discussion.

```
github.com/go-chi/chi/v5
modernc.org/sqlite      (pure Go — the build is CGO_ENABLED=0)
golang.org/x/crypto
github.com/google/uuid
```

**Errors.** Wrap with context: `fmt.Errorf("descriptive context: %w", err)`.
Never `_` an error on a path where the result is shown to someone — a discarded
count renders as `0`, which is a claim, not an absence.

**If you cannot store it, do not report success.** Any path that claims to have
kept data safe must fail loudly when it has not. A held submission whose write
failed once returned 302 and a success page, destroying the submission inside the
feature built to stop exactly that.

**Log-and-continue needs an aggregate signal.** Degrading one panel to zero is
reasonable; degrading nine independently renders a broken database as a page
reading `0` everywhere, indistinguishable from a fresh install. If a page can
degrade, it must be able to say that it did — see `PageData.Degraded`.

**Error messages must not point away from the truth.** `RestoreSubmission` once
reported "could not be restored" for a submission that *was* restored, sending
the operator to search the wrong screen. An inaccurate message is worse than a
generic one.

**Panics.** Only in `config.Load()`, for a missing required env var. Fail fast at
startup, never during a request. A `recover()` only catches panics in its *own*
goroutine.

**Goroutines.** Anything outliving a request gets a deferred `recover()` inside
it. Background loops (session cleanup, quarantine purge, digest) log and continue
rather than dying on one iteration.

**Time.** Inject the clock where behaviour depends on it — `func() time.Time`, or
a `now` parameter as `ageSince` and `PurgeHeldOlderThan` do — so tests never
sleep.

**Determinism.** Go randomises map iteration. Anything whose order is
observable — rendered output, a returned slice, a test assertion — sorts first.
`spam.Detail` sorts field names for this reason.

**Prefer a defined type over a documented string set.** `spam.Rule` is a type
because its documented list of valid values went stale inside the very PR that
wrote it. A `map[spam.Rule]string` for display makes a missing entry a
compile-time prompt instead of a blank icon nobody notices.

**Never hand-count in a comment.** "these five handlers", "thirteen structs",
"sixteen call sites" — all four such counts in this repo were wrong on arrival.
Say "every handler".

**Formatting.** `gofmt` and `go vet` must be clean. `gofmt` catches things the
compiler will not: after the module rename, every import block needed regrouping
and `go build` was perfectly happy.

---

## 5. Data and SQLite

**Schema.** `CREATE TABLE IF NOT EXISTS` in the `schema` constant; column
additions in `runAlterMigrations`, which swallows only `"duplicate column"`. **A
new column goes in both** — the CREATE TABLE for fresh databases, the ALTER for
existing ones. An index over a newly-ALTERed column must run *after* the ALTER
pass, not in `schema`.

**Placeholders.** `?` always; never build SQL by concatenation. Scope an
`IN (?,?,?)` by owner (`WHERE form_id = ? AND id IN (…)`) so a foreign id cannot
reach another form's rows.

**Bound parameters cap at 32766.** An `IN (…)` built from an unbounded list must
batch, or be rewritten as a set-based statement. "Empty quarantine" enumerated
every id and therefore failed on exactly the queue large enough to need
emptying — see `DeleteAllHeld`.

**Timestamps.** Always `.UTC().Format(sqliteTime)`. Handing a `time.Time` to the
driver stringifies it as `"… +0000 UTC"`, which `date()` cannot parse; and since
`created_at` is TEXT, a range comparison against a differently-formatted value is
silently meaningless.

**Ordering.** `ORDER BY created_at` alone is not stable — submissions arriving in
the same second tie, and tied rows can come back in a different order per query,
so a `LIMIT/OFFSET` page can repeat or skip a row. Always tie-break:
`ORDER BY created_at DESC, id`.

**Reads populate the whole struct.** A query selecting a subset of a type's
columns returns a value whose other fields are silently zero. That shipped a
screen reading `score 0` beside a breakdown summing to 11. Use the shared column
list and scan helper.

**In-memory tests.** `store.New(":memory:")` caps the pool at one connection;
every additional connection to an in-memory database gets its own empty one.

**Search.** FTS5 external-content index over `submissions.data`, synced by
triggers. `COUNT(*)` on that table counts the **content** table, not the index —
use `submissions_fts_docsize` for staleness. All user input goes through
`ftsQuery` before `MATCH`: raw input is a syntax error, not a no-match. Guard
both directions — over-sanitising silently breaks search for `O'Brien` and
`order_id`.

---

## 6. HTTP, handlers and the frontend

**Handlers** embed `handler.Base`. Build page state with
`Shell(w, r, title, active)` — on a path that renders, since it consumes the
flash cookie — and render with `Render`.

**Mutations are POST-only**, inside the `auth.RequireAuth` group. There is no
CSRF token; the session cookie is `SameSite=Lax` and that is the whole defence.

**A permissive rule matches a canonical field; a restrictive rule may scan
everything.** Submitters choose their own field names. An allow rule that
scanned every field turned any *mention* of an allowlisted address into a
skeleton key — append one junk field and skip the blocklist and all scoring.
Block rules still scan everything, because a spammer will not helpfully put
their address in the field we check.

**Never derive an authorization or bypass decision from `X-Forwarded-For`.** It
is one header. Fine for rate-limiting and logging; never for "skip the checks".

**Limits and headers.** `http.MaxBytesReader` 64KB, with the 100MB
backup-import exception. The CSP is
`default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'`
— note there is no `font-src`, so it falls back to `default-src 'self'` and
**anything fetched from a CDN is blocked by our own header.** Vendor it.

**Never block the response on email or a webhook.** Both go in a goroutine. And
whatever a path withholds, the path that reverses it must deliver *all* of —
restoring a held submission sends the email *and* the webhook.

**Templates.** `base.html` is parsed once and cloned per page; each page defines
`{{define "content"}}`. New pages join `basePages` in `main.go` **and**
`populatedPageData` in `internal/handler/templates_test.go` (which mirrors the
list by hand, since a test cannot import `main`; the test fails on a page with no
fixture). Icons come from the
`{{template "icons"}}` sprite — regenerate with `scripts/build-icons.sh`, never
hand-write an inline `<svg>` path.

**CSS and JS** are embedded under `//go:embed static/*` and served from
`/static/` with a content hash, so they can be cached hard. **No `<style>` block
in any template** — tokens living in two places is how `login.html` drifted from
the admin. (Inline `style=` attributes are used freely for one-off layout; the
rule is about the token layer.) `static/app.js` is plain delegated vanilla JS
with no build step, and everything in it is an *enhancement*: every feature is a
real link or form post that works server-rendered.

**`fetch` follows redirects.** An expired session answering a fragment request
with a 302 to `/admin/login` returns 200, so `r.ok` is true and the login page
lands inside a drawer where no close control exists. `RequireAuth` returns 401
for `X-Fragment`; clients also check `r.redirected`.

**Assume a non-secure origin.** `navigator.clipboard` is undefined on plain http
except localhost, which this project supports. A silently-swallowed clipboard
call leaves a button labelled "Copy" doing nothing at all.

---

## 7. Testing

```
1  Write the test file before the implementation file
2  Run go test to confirm it FAILS before implementing
3  Write the minimum implementation to make it pass
4  Every exported function has at least one test
5  Every error path has a test
6  Table-driven tests for multiple input cases
7  t.Parallel() in every test that does not share mutable state
8  go test -race must pass
9  No time.Sleep — inject time instead
10 foo_test.go beside foo.go
```

**Assert the behaviour, not its absence.** A test checking only that a row is
missing from the inbox passes whether the submission was quarantined *or*
destroyed. Eight tests here did exactly that. Ask of every assertion: what
regression would still slip past this?

**Verify the test fails first.** Not as ceremony — as proof it tests anything.
The template-execution test was written to catch a renamed field, and did not:
zero-valued structs skip every `{{if}}` and `{{range}}` body, so most of the
template never ran. Fixtures must be populated enough to take each branch.

**Prove a fix with the reproduction.** For a bug found in review, reproduce it
first, then show the reproduction flipping.

---

## 8. Project layout

```
main.go                  wiring only
internal/
  config/                env → Config
  store/                 all SQLite
  auth/ flash/           sessions, one-time messages
  spam/ filter/          scoring; operator block/allow rules
  ratelimit/             per-IP token bucket, in-process
  mail/ webhook/ backup/ broadcaster/
  handler/               HTTP
templates/               html/template, embedded
static/                  app.css, app.js, fonts — embedded
docs/                    index.html (GitHub Pages) + design/specs/
scripts/                 build-icons.sh, changelog.sh
```

---

## 9. Git

```
feat:     add store package with user and form CRUD
fix:      handle empty redirect URL in submit handler
test:     add table-driven tests for config loading
refactor: replace HMAC auth with DB-backed sessions
docs:     add README with deployment guide
chore:    add Makefile with test and build targets
```

One concern per commit. A mechanical repo-wide change — a rename, a formatting
pass — goes in its own commit so the substantive diff stays readable.
