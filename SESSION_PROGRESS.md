# Session Progress — Nocturne Admin Redesign

Branch: `refactor/seal-screening-decision` (merged from `feat/nocturne-redesign`)
Spec: `docs/design/specs/2026-09-09-nocturne-admin-redesign-design.md`
Handoff: `reference/design_handoff_dsforms_admin/`

## Phases

| Phase | Content | Status |
|---|---|---|
| 0 | Vendored assets (Inter, Phosphor), Nocturne tokens, `base.html` shell | **done** |
| 1 | `screen.Decide`/`Signal`/`DefaultThreshold`, `config.SpamThreshold`, schema, store methods | **done** |
| 2 | Submit-handler rewiring (allow → block → honeypot → score → hold) | not started |
| 3 | Forms, form detail, reader drawer | **done** |
| 4 | Quarantine + breakdown, Filter rules | **done** |
| 5 | Home aggregates, inline-SVG charts, rate-limit snapshot | **done** |
| 6 | Form settings, Waitlists, Users, Backups, Login, empty states | **done** |
| 7 | FTS5 search (⌘K), digest email, false-positive reporting | **done** |
| 8 | Landing page (`docs/index.html`) | **done** |

## Done since

- **Module path** `github.com/youruser/dsforms` → `github.com/barancezayirli/dsforms`.
  The `go mod init` placeholder from commit `fcb19d1` never matched the repo URL, which
  made `go get github.com/barancezayirli/dsforms` fail outright. It is one isolated
  commit on this branch, so it still reviews and reverts independently.

## Working agreement moved to AGENT.md

`CLAUDE.md` is now a pointer; the working agreement lives in **AGENT.md**. It was
rewritten rather than moved: the development workflow is nine ordered steps with
the PR review marked non-skippable, and it gained an architecture section and the
rules this review actually produced — each stated with the bug behind it.

The prompt for it was this branch: the review step existed in `CLAUDE.md` and was
skipped anyway, and the branch came back with ~50 findings including a spam-filter
bypass. Two claims in the first draft were removed for being false on inspection,
which is the argument for keeping one copy rather than two.

## Second review pass

The fix pass was itself reviewed by six agents, which returned ~45 findings
including three criticals. The headline: **the security fix had not closed the
hole it was written for.** Allow-rule matching was restricted to fields *named*
`email`, case-insensitively — but HTTP field names are case-sensitive, so
`email=spam&Email=allowlisted` walked straight through. Reproduced live, then
closed.

The pattern is worth keeping: the first pass fixed each finding *as reported*
rather than fixing the mechanism behind it, and each regression test enumerated
the report rather than the mechanism, so the tests passed against code that was
still open. the sender now has a single definition (now `screen`-sealed) of "the sender",
shared with the submit handler, and returns none/one/**ambiguous** — ambiguity
stays unresolved, because every tie-break has a side an attacker can land on.

Also fixed: a failed restore email silently skipped the withheld webhook; a
double-clicked restore reported failure for a submission that *was* restored;
`overviewData` shadowed `PageData.Degraded` so the banner could never fire on
the page it was built for; four of six read paths returned partially-populated
Submissions; `CreateSession` bound a raw `time.Time` (pre-existing — a 2-hour
session was born expired on any negative-offset host, latent only because both
callers use 30 days); four background loops had no `recover()`; and the
populated-fixture template test had not generalised, leaving `search.html`
rendering its empty state.

Verified end-to-end on a fresh database, not just by test: the bypass returns
400, an allowlisted sender in the canonical field is still accepted, a restored
submission keeps its `spam_score`, a double-click says "Already restored", and
dropping `waitlist_entries` makes the degraded banner appear on all three pages
that previously read zero in silence.

Three of the AGENT.md corrections were errors written in that same session,
including a `Check` "compile-time prompt" guarantee that Go does not
provide. `AllChecks` plus an AST-derived completeness test now builds the
guarantee that was previously only asserted.

## Third review pass

Six agents again, ~50 findings. **The filter bypass was open for the third
consecutive round**, each time one layer beneath the previous fix: round 1 fixed
"scan every field" and left the field *name*; round 2 fixed the name and left the
*value*. The value had three incompatible definitions —
`mail.ParseAddress` for rule storage, a shape test for matching, and
`strings.ToLower` for folding on both sides.

Two live bypasses, both reproduced, both closed:

- `MİKE@works.com` (U+0130) and the Kelvin sign U+212A lower into ASCII, so any
  allowlisted address containing i, k or s was reachable by one the operator
  never allowlisted.
- `Bot <bot@example.com>` validated, stored and displayed as the sender while
  being invisible to every block rule, because the shape test rejected any value
  containing a space.

a single `Canonical` is now the definition (now `screen`-sealed), folding ASCII-only. The
regression test is a property — no byte-distinct value may canonicalise onto an
honest address — rather than the list of confusables known today, which is what
left it open twice.

Round 2's own fixes had also created three symmetrical bugs, each fixing one
direction of an asymmetry without asking what the opposite case then reported:
"Already restored — it is in the inbox" for a submission the retention sweep had
permanently deleted; `DeleteHeld` reporting 0 for a thousand rows it had already
committed; and a panic making the broadcaster read its queue as empty and retry
a poisoned row forever without ever incrementing its attempts.

Four invariants that were enforced by comments are now enforced by tests, each
verified against the real bug: no hand-written submissions SELECT (AST scan, 8
reads), no shadowed embedded field (catches `overviewData.Degraded` at
`71ced73`), every `Shell(…, active)` names a `navGroups` key (28 sites), and
`pageMarkers` covers every base page. The first version of the SELECT scan passed
while inspecting *zero* queries, so each of these now has a count floor — a
guarantee test that asserts nothing is worse than no test.

Nine documentation claims corrected, six of them written during round 2's own
correction pass — including a dependency diagram that still said `handler`
imports `mail` (it does not; `go list -deps` is the check) and two new
hand-counts written inside the comments forbidding hand-counts.

Two rules added to AGENT.md §4: **in a switch over a closed value set the safe
outcome is never `default`** (three instances on this branch), and **close the
mechanism, not the reported instance**.

## Fourth pass — sealing the decision, and reviewing the seal

`feat/nocturne-redesign` was merged to local `main`, unpushed. Three review
rounds had each found a live filter bypass one layer beneath the previous fix,
so the next step was structural rather than another fix pass.

**The refactor.** `internal/filter` and `internal/spam` are gone. The hold/accept
decision now lives behind one entry point in `internal/screen`, with the
implementation under `internal/screen/internal/{addr,rules,score,repeat}` — which
Go forbids any other package from importing. The three-way split that produced a
bypass in three consecutive rounds does not fail review now; it fails to compile.

Behaviour is unchanged and that is proved, not asserted: a 367-case golden
recorded before any code moved is byte-identical after. `spam.Rule` became
`screen.Check`, because `filter.Rule` (the operator's rule) already owned `Rule`
and one package cannot have two.

**The fourth review round** ran six agents. Three completed; I stopped the rest
because they were mutation-testing in the shared working tree, which destabilised
it and set off the security scanner repeatedly. That accident was the round's
best evidence: six regressions were injected into live code and the defences
caught five, including both historical bypasses. The sixth — the repeat-IP
threshold — produced no golden diff, which is a real hole and is now closed.

Fixed this pass:

- `matches()` logged "unknown type" for *every* non-matching email or domain
  rule, burying the one event that line exists to make loud. The email and
  domain cases fell out of their loops without returning.
- A zero threshold held every submission with an empty breakdown. `Decide` now
  clamps, and `MinThreshold`/`MaxThreshold` live in `screen` instead of being
  restated in the settings handler and in config. The fuzzer's own threshold
  clamp is deleted — it was only ever checking the space where callers were
  already correct.
- **Filter rules that can never match are now surfaced.** A rule stored under an
  older normalisation (`bot@localhost`, accepted before the address definition
  was unified) is permanently inert, and a *block* rule in that state fails open
  while appearing active. `screen.CheckRules` round-trips each stored value
  through the validator; the rules screen names the affected rules and the
  startup log reports a count.
- The golden now samples both sides of the repeat boundary.
- `Verdict.Matched` distinguishes "a rule decided this" from "the rule had an
  empty ID", so a lost hit count is loud.
- AGENT.md described a deleted architecture in seven places, including the
  defined-type rule, which pointed at `screen.Rule` — a different type from the
  one it means.

## Fifth pass — narrowing the store surface

Branch `refactor/narrow-store-surface`. `internal/store` was the last shared
kernel: every handler embedded `Base{Store *store.Store}` and could reach every
method on it regardless of the few it called. Each handler now declares a
consumer interface naming exactly what it uses, `Base` holds a one-method
`NavCounter`, and `internal/backup` declares its own two-method interface — which
dropped `store` from its imports entirely, so it is now a leaf.

The method lists were generated from the call graph and the signatures from
`go doc`, not transcribed; `main.go`'s `var _` block then proves the generation
was faithful. Worst-case reachable surface per handler fell from every method to
twenty, best case to one.

**What the refactor introduced, and what caught it.** Swapping a pointer field
for an interface field moves a class of error from compile time to run time: a
struct literal that omits a field zeroes it, and `go build ./...` reported
success on a binary in which most handlers held a nil store. Nothing failed
until a request arrived. `TestEveryStorageFieldIsWired` reads the required set
out of `main.go`'s assertion block — authoritative and compiler-checked — and
checks every construction site against it.

**The review found blind spots in all three new guards, and each was the same
shape as the bug it was written to catch.** Recorded because the pattern is the
point: the wiring test keyed on a naming convention, so renaming an interface
walked past it; it required only directly-declared fields, so dropping
`Base: base` zeroed seven fields and passed; it counted sites rather than
diffing the set, so a duplicate literal paid for a missing one. The
concrete-store scan matched the identifier `store`, so an aliased second import
reinstated `*store.Store` with the suite green — and the first fix for that was
itself incomplete, because a file may import one package under several names and
the helper returned only the first. The upload guard checked one link of a
four-link seam. All are closed, each verified by applying the regression and
watching the named test fail. The detector now has a positive control, since a
count floor measures only that files were parsed and cannot see a predicate that
has stopped matching.

Also fixed here: database restore had been broken since the Nocturne port
(`664bfbc`) — the template posted `name="backup"`, the handler read `"file"`.
Found by running the thing per step 5, not by any test.

## Deferred from the fifth pass

Nothing below is caused by the refactor; the narrowing surfaced them.

| Item | Why deferred | Target |
|---|---|---|
| **`backup.Import` leaves the process holding a closed database** | Three exit paths after the live handle is closed return without reopening — stale-WAL removal, `os.Rename` (the realistic one: `os.CreateTemp("")` puts the upload on `/tmp` while the DB is on a volume, so Docker gets `EXDEV`), and `Reopen` itself. `store.Reopen` only assigns on success, so every route 500s until a restart, `/healthz` still says `ok`, and the flash blames the uploaded file — which passed validation. A rollback to the original path recovers cleanly when tried. This is the most destructive operation in the product and deserves its own branch and its own tests, not a rider on a refactor. | **Next session — highest priority** |
| `/healthz` never touches the database | It is the failure detector for the row above and reports healthy while every real request fails. A `SELECT 1` fixes it. Pairs naturally with that work. | Next session |
| `_subject` is documented and read by nobody | `README.md` offers it as the notification subject; `internalFields` strips it before storage and the subject is hardcoded in `mail`. A user following the README loses the value twice, silently — worse than the backup bug, which at least printed something. `submit_test.go` asserts the discard, locking it in. Either implement or delete the row. | A follow-up PR |
| The waitlist snippet omits `_honeypot` | `waitlist_submit.go` reads it and `form_edit.html` emits it for `/f/`, but `waitlist_edit.html` does not. The waitlist route runs no screener, so the honeypot is its only filter — every operator who copies the offered snippet ships a signup form with none. | A follow-up PR |
| `rules.html` posts no `note` | The handler reads one and the template renders it, so rules added from the quarantine screen carry provenance and identical rules added from the Rules page render bare. | A follow-up PR |
| `sql.ErrNoRows` is an unwritten term of the new interfaces | `AdminHandler` tests `errors.Is(err, sql.ErrNoRows)` against interface results. That was a private arrangement between two concrete types; it is now the load-bearing contract of a published interface, expressed nowhere in it. Any implementation not wrapping a `database/sql` sentinel turns every 404 into a 500. A `store.ErrNotFound` is the fix and touches every handler. | A follow-up PR |
| Nothing stops the interfaces re-widening | `TestNoHandlerHoldsTheConcreteStore` catches a field typed `*store.Store`; it does not catch `SearchStore` growing to twenty methods nobody calls. Adding unused methods keeps the suite green. A scan asserting every method declared on an `XStore` is actually called through that field closes it, in the direction the drift runs. | A follow-up PR |
| `AdminStore` (20) and `QuarantineStore` (14) want splitting | `AdminHandler` is two handlers: the forms half and the submissions half share only `GetForm`, and the routes already draw the line. `QuarantineStore` splits into a read-mostly review queue and a three-method rule-mutation surface with zero overlap — worth separating, since a rule write is what can open a fail-open block rule. | A follow-up PR |
| `screen.Rule`'s invariant has no owner | `Rule.Value` is documented as normalised by `Validate` before storage, and `Decide` passes rules straight to `Match`. The only producer used to be the store, which validates every write; the interfaces make that producer pluggable, so the type now permits an unvalidated block rule — which fails open. Not reachable in production. Cheapest close, consistent with the seal doctrine: have `Decide` re-validate inbound rule values. | A follow-up PR |
| `main()`'s wiring and all routes have no executable coverage | `newRouter()` stops after middleware and `/healthz`; every handler construction and route registration lives inline in `main()`, which no test can call. So the AST scan is not the primary check on the wiring, it is the only one. Extracting `func routes(...) *chi.Mux` would let one test drive the real table — and would catch a handler bound to the wrong route, or a route registered outside the auth group, neither of which anything notices today. | A follow-up PR |
| `store.Reopen` mutates `s.db` under a live server | No synchronisation, while every other method reads that field. Pre-existing; surfaced by reading `backup.Import` closely. Belongs with the restore work above. | With the restore fix |
| `Base.Shell`'s nil check misses a typed nil | `b.Nav == nil` is false for a non-nil interface holding a nil pointer, so that case panics rather than degrading. Latent: `main` exits fatally if the store cannot open, so `s` is never nil. | A follow-up PR |

## Accepted risks

| Risk | Why accepted |
|---|---|
| IP and CIDR **allow** rules trust `X-Forwarded-For` | `ExtractIP` has trusted the header unconditionally since before this work, and dsforms is designed to sit behind a reverse proxy that sets it. Allow rules make that a scoring bypass rather than only an attribution problem, so it is worth knowing: an IP allowlist is only safe behind a proxy you control. Email and domain allow rules are not affected — they match the sender field, which the submit handler validates. Re-architecting XFF trust needs a proxy-configuration decision and is out of scope here. |

## Deferred items

| Item | Why deferred | Target |
|---|---|---|
| Retyping `screen.Rule.Kind` / `.Type` as defined types (was `filter.Rule`) | They are the "documented string set" AGENT.md §4 names, and `matches` fails open on an unknown type — an in-memory rule with a typo'd Type silently matches nothing, which for a *block* rule is a bypass rather than a no-op. The DB `CHECK` constraints make it unreachable today, so the invariant lives in SQLite rather than in Go. The constants already exist; the change is mechanical. | A follow-up PR |
| Deriving the SVG viewBox strings from the geometry constants | `SparkViewBox()` and friends return hand-written literals (`"0 0 220 40"`) while the paths are generated from `sparkWidth`/`sparkHeight`. Changing a constant updates every path and leaves the viewBox behind — the exact drift the function's own comment says it prevents. Three lines. | A follow-up PR |
| A "resend notification" action for restored submissions | `notified = 0` on a restored row records a notification that was never sent, but nothing reads the column — no sweep, no retry, no admin action — so a failed send is not retried. Needs UI design. The misleading comment claiming otherwise has been removed. | A follow-up PR |
| Wiring `docs/screenshots/` into the landing page | The five captures (dashboard, reader, submission, form-edit, login) are committed, but `docs/index.html` references none of them — the Admin UI section still describes the screens in cards instead of showing them. Only the page markup is outstanding. | A follow-up PR |

## Open questions

_None currently — D1–D10 in the spec settle the handoff's open items._
