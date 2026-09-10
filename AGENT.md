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
make test                   # go test ./... -race -count=1
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
main.go        config, store, handler construction, routes, CLI — no logic
  │            also constructs mail, webhook and broadcaster, and wires them
  │            into handler through the interfaces handler declares
  └── handler  HTTP: request → store/domain calls → template
        ├── auth                        wraps store (sessions)
        ├── backup                      imports nothing from internal/
        ├── store                       every SQL statement in the project
        ├── screen                      the hold/accept decision, sealed
        └── ratelimit · flash · safe
```

Verified with `go list -f '{{join .Imports "\n"}}'`, not from memory:

- `handler` → auth, backup, flash, ratelimit, safe, screen, store
- `store` → screen · `config` → screen · `broadcaster` → safe, store
- `auth`, `mail`, `webhook` → store · `ratelimit` → safe
- `backup`, `flash` and `safe` import nothing from `internal/`

`backup` used to import `store`, for one parameter: `Import(s *store.Store, …)`,
which called two methods on it. Naming those two in an interface `backup`
declares itself dropped the import entirely. That is the rule below applied to a
package that was already written — the dependency was never real, only spelled
that way.

Note what `handler` does *not* import: **`mail`, `webhook` and `broadcaster`**.
It reaches all three only through interfaces it declares itself, which is the
rule below made concrete. Run the command rather than trusting this block — the
diagram claimed `mail` for two rounds because nobody did, and it claimed
`broadcaster` imports `screen` until someone did.

**Rules that follow:**

- **Leaves stay leaves.** `flash` and `safe` import nothing from `internal/` at
  all; `ratelimit` imports only `safe`. That is what makes them testable without
  a database.

  `safe` is the one permitted exception to a leaf importing anything, because it
  has no dependencies of its own and every layer needs it: a goroutine anywhere
  that is not guarded can take the process down. Anything else in a leaf is a
  design error.

- **`screen` is a sealed subtree, not a leaf.** Its public surface is one file;
  the implementation lives in `internal/screen/internal/{addr,rules,score,repeat}`,
  which Go forbids any package outside `internal/screen` from importing. That is
  a compiler rule. It exists because the hold/accept decision previously had
  three owners — the handler decided sender validity, the store decided the
  stored form of a rule, the matcher decided the compared form — and a filter
  bypass was found in the seam between two of them in three consecutive review
  rounds. `store` and `config` import `screen` for the types they persist and the
  threshold bounds; nothing can reach past it.
- **All SQL lives in `internal/store`.** Handlers never touch `db.Query`. A
  handler that needs data needs a store method.
- **Interfaces are declared by the consumer**, never by the implementer.
  `handler` declares an interface for each service capability it consumes;
  `auth` declares `SessionStore`; `broadcaster` declares its own `Store` and
  `Mailer`. Satisfaction is checked by the compiler where `main.go` wires the
  concrete type in, and pinned explicitly in the `var _` block at the top of
  `main.go` — a new consumer interface belongs there too. `broadcaster` pins its
  own with `var _ Store = (*store.Store)(nil)`.
- **`main.go` holds no business logic**, and there is no global state outside it.

### Adding a package

Check first whether the logic belongs in an existing leaf. A package earns its
place when it has its own vocabulary and is testable alone — `internal/screen`
qualified (the whole hold/accept decision, no database); one helper function
usually would not.

`internal/safe` is the deliberate exception and worth stating so nobody deletes
it on that rule: it is a single function, but it is needed from `main`,
`handler`, `broadcaster` and `ratelimit`, and every other home would invert the
dependency direction.

### Modularity: check before you write

Before writing a new function, grep for the same *shape*. If it exists, use it.
If it exists twice, consolidate before adding a third.

This repo keeps relearning it. A JSON-decode-with-fallback was inlined at every
call site that needed it; a session-cookie helper was copied into each handler
test; `Title/Active/CurrentUser/Flash` was restated in every page struct until
`PageData` absorbed it (and `Base` absorbed the per-handler dependencies); and `NavCounts` existed twice — once in `store`, once in
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

**Panics.** Only at startup, and only for something a running process cannot
fix: in `config` for a missing required env var **and for a malformed integer
one** (`envOrInt` — a bad `BROADCAST_MAX_ATTEMPTS` or `SPAM_THRESHOLD` is a
refusal to start, not a fallback), and in the leaf constructors
(`ratelimit.NewLimiter`, `ratelimit.NewLoginGuard`, `screen.New`) on a
programmer-error argument. Never during a request. `screen.New` is the reachable
one — it panics via the tracker it constructs, which is itself sealed.

**Goroutines.** Run anything outliving a request through `safe.Do`, which
recovers and logs — a `recover()` only catches panics in its *own* goroutine, so
an unguarded worker takes the whole process down with it. Wrap a background
*loop* per iteration rather than per goroutine, so one bad tick costs one tick.

Two caveats learned the hard way. Recovering is not enough on its own: the
broadcaster's loop read the recovered iteration's zero values as "the queue is
empty" and retried the same poisoned row forever, so a guarded loop must still
surface the panic to its own control flow. And "every goroutine is guarded" is a
claim to check, not to assert — `grep 'go func'` found two loops in `ratelimit`
that this rule had already been claiming for a round.

**Time.** Inject the clock where behaviour depends on it — `func() time.Time` as
`ratelimit.NewLimiter` takes, a `now` parameter as `ageSince` takes, or a
precomputed cutoff as `PurgeHeldOlderThan` takes — so tests never sleep. Waiting
on a timeout to prove an *absence* is a sleep in disguise and passes for the
wrong reason under load.

**Determinism.** Go randomises map iteration. Anything whose order is
observable — rendered output, a returned slice, a test assertion — sorts first.
The content scorer sorts field names for this reason, so `Verdict.Signals`
arrives in a stable order.

**Prefer a defined type over a documented string set.** `screen.Check` is a type
because its documented list of valid values went stale inside the very PR that
wrote it. (It was called `spam.Rule` then. It is `Check` now precisely because
`screen.Rule` — the operator's allow/block rule — is a different thing, and one
package cannot own two `Rule`s. Do not apply this paragraph to `screen.Rule`.)

The type alone buys less than it looks. Go does not exhaustiveness-check a map
literal keyed by a named type, or a switch over one, so a new constant with no
display entry compiles cleanly and `go vet` is silent — the blank icon the type
was supposed to prevent. What closes it is `screen.AllChecks` next to the constants,
ranged by the coverage test, plus a test that derives the constant list from the
package's own AST so the slice cannot fall behind either. A guarantee like this
has to be built; naming the type is only the first half.

**In a switch over a closed value set, the safe outcome is never `default`.**
Name every case and let the default deny, log, or skip. Written from three
instances on one branch: a validator whose `default` was the *accepting* branch,
a rules screen that filed an unrecognised kind under "Blocked" while the matcher
ignored it, and a matcher that fell through to "no match" — which for a block
rule is fail-open. The enumerated cases get handled; the remainder silently takes
whichever path was written last.

**Close the mechanism, not the reported instance.** A review names an example; fix
the general form. This branch had the same filter bypass found open in three
consecutive rounds because each fix addressed the layer that was reported — scan
every field, then the field *name*, then the field *value* — and each regression
test enumerated that layer's variants. Before committing a fix, ask what layer
sits beneath the one you just closed, and write the test as a property rather
than as the list of cases you happened to think of.

**Never hand-count in a comment.** "these five handlers", "thirteen structs",
"sixteen call sites" — every such count in this repo has been wrong on arrival,
including one that survived the commit which fixed the others. Say "every
handler".

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

**Timestamps.** Always `sqliteTimestamp(t)`. Handing a `time.Time` to the driver
stringifies it with an offset (`"2026-09-09T17:22:49-07:00"`, or `"… +0000 UTC"`
depending on the value), which `date()` cannot parse; and since these columns are
TEXT, a range comparison against a differently-formatted value is a string
comparison that is silently meaningless. The helper folds in the `.UTC()`
unconditionally, because four call sites were correct only by tracing the value
back to a `time.Now().UTC()` a few lines up.

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

**And "canonical" has to be canonical.** The first fix restricted allow rules to
fields *named* `email`, case-insensitively, which left the hole open: HTTP field
names are case-sensitive, so `email` and `Email` are two fields one submission
can carry at once. The sender has a single definition, sealed inside
`internal/screen`, reached by the submit handler through `screen.SenderOK`, and it
returns three states — none, one, ambiguous. Ambiguous stays unresolved: every tie-break has a side the attacker
can land on, so two claimants means we do not know who sent this, and a
permissive rule never fires on a guess.

The general form, which is the part worth carrying to the next fix: **closing a
hole means closing the mechanism, not the reported instance.** The report said
"any junk field"; the mechanism was "what counts as the sender field", and only
the first was fixed. The regression test enumerated the reported case, so it
passed against code that was still wide open.

**Treat `X-Forwarded-For` as attacker-controlled unless a proxy you control
overwrites it.** `ExtractIP` trusts it unconditionally, which is correct only
behind such a proxy — and dsforms is designed to sit behind one. Fine for
rate-limiting and logging either way.

The consequence to hold onto: an IP or CIDR **allow** rule turns that trust into
a scoring bypass, since one header then skips the block list and all scoring.
That is a recorded accepted risk in `SESSION_PROGRESS.md`, not an oversight —
but do not add a *new* decision that reads this header, and prefer email or
domain allow rules, which match the validated sender field instead.

**Limits and headers.** `http.MaxBytesReader` 64KB, with the 100MB
backup-import exception. The CSP is
`default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'`
— note there is no `font-src`, so it falls back to `default-src 'self'` and
**anything fetched from a CDN is blocked by our own header.** Vendor it.

**Never block the response on email or a webhook.** Both go in a goroutine. And
whatever a path withholds, the path that reverses it must deliver *all* of —
restoring a held submission sends the email *and* the webhook.

**Templates.** `base.html` is parsed once and cloned per page; each page defines
`{{define "content"}}`. A new page joins **three** lists: `basePages` in `main.go`, and both
`basePageNames` and `populatedPageData` in
`internal/handler/templates_test.go` — `basePageNames` mirrors `basePages` by
hand because a test cannot import `main`. `TestRealTemplatesCoverEveryPage`
walks the directory and catches a missing mirror entry; the execution test
catches a missing fixture. Icons come from the
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

**A guard nobody has watched fail is not a guard.** Structural tests — the AST
scans, the wiring checks — pass in two different worlds: the property holds, or
the matcher has stopped matching. A count floor tells the two apart only when the
floor is right; it measures how many nodes were visited, so it is blind to a
predicate that has quietly died. On this repo one scan reported "inspected 47
structs" while being unable to see an aliased import, and an earlier one
inspected *zero* queries because it looked for string literals when every query
is a concatenation.

So: express the predicate as a function, give it fixtures that must trip it and
fixtures that must not, and run them — `internal/astcheck` has the harness. Then
break the production code deliberately and watch the named test fail with the
right message before you commit. Every guard on this branch that skipped that
step turned out hollow; every one that did it caught a real regression later.

**Characterise what you depend on, not just what you wrote.** A test that pins
an assumption about SQLite or the filesystem is worth as much as one that pins
our own behaviour, because the sealed packages are exactly the ones whose
correctness rests entirely outside them. `PRAGMA wal_checkpoint` reports
contention as a *row value* with a nil error; `os.Rename` overwrites its
destination; opening a missing SQLite file creates it. Each of those was assumed
wrongly here, in reviewed and committed code, and each is four lines to pin —
see `internal/backup/contracts_test.go`. Isolation bounds what can be wrong
*together*; it does nothing about what you have to get right *alone*.

---

## 8. Project layout

```
main.go                  wiring only
internal/
  config/                env → Config
  store/                 all SQLite
  auth/ flash/           sessions, one-time messages
  screen/                the hold/accept decision; implementation sealed
                         under screen/internal/{addr,rules,score,repeat}
  ratelimit/             per-IP token bucket, in-process
  mail/ webhook/ backup/ broadcaster/
  safe/                  run a func without letting a panic escape
  handler/               HTTP
templates/               html/template, embedded
static/                  app.css, app.js, fonts — embedded
docs/                    index.html (GitHub Pages), design/specs/, screenshots/
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
