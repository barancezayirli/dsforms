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

## Sixth pass — a failed restore must leave a working database

Branch `fix/restore-leaves-closed-database`.

`backup.Import` closed the live handle and then had three returns before it
reopened anything, and `store.Reopen` only assigns on success — so any of them
left the process holding a closed handle. Reproduced directly:

```
Import returned:                import: reopen: simulated reopen failure
ListForms after failed Import:  list forms: sql: database is closed
ListForms, second attempt:      list forms: sql: database is closed
```

Two things were worse than the review reported. The `Reopen` path was **data
loss, not downtime** — `os.Rename` had already overwritten the live database, so
there was no original to go back to. And the cross-device case was **the default
deployment**: the handler staged uploads in `/tmp` via `os.CreateTemp("", …)`
while `DB_PATH` defaults to `/data/dsforms.db`, so the rename failed with `EXDEV`
in any container. It went unnoticed only because the field-name bug meant this
code never ran.

The previous database is now parked under `.rollback` and deleted only once the
replacement has actually opened; every failure after the close funnels through
one `rollBack` helper, and `Import` cannot return without either a working
database or `ErrUnavailable`. Uploads are staged beside the database, so the
swap is a same-filesystem rename by construction.

Three sentinels replace one error value, because the three outcomes need
opposite operator responses — the old single message told an operator whose
service was down that their file was probably corrupt, sending them to re-export
against a process that could not answer.

A failed checkpoint is now fatal rather than logged. It used to warn that
unflushed data may be lost and then delete the WAL, which is what lost it; with
a rollback in play it would have restored a database stripped of its own
unflushed frames — a quiet data loss dressed as a recovery.

`/healthz` asks the database instead of reporting that the HTTP server is
listening. That is the detector this whole class of failure never had.

## Sixth pass, review round

Three agents reviewed the restore fix and found real defects in it, two of them
data loss introduced by the fix itself. Recorded because the pattern repeats:

- **A leftover `.rollback` was silently destroyed.** The park is a rename, and
  rename overwrites. A process killed between the park and the swap leaves the
  real database at `.rollback` and nothing at `dbPath`; the container restarts,
  `store.New` creates an empty database and re-seeds the default admin, and the
  operator's natural next move — restore a backup — renames that empty database
  over the last copy of their data. `Import` now refuses to start when a parked
  file exists. My own test had used a *directory* at that path, which fails the
  rename for unrelated reasons and so hid that a *file* succeeds.
- **`rollBack` removed the live database before renaming the original back.** The
  comment said the rename had "nowhere to land", which is false — rename replaces
  its destination, as the park twenty lines above relies on. The removal opened a
  window with no database at `dbPath` at all. Now one atomic rename.
- **`Reopen` is not evidence of a rollback.** SQLite creates the file if missing,
  so reopening an absent `dbPath` manufactures an empty database and returns nil.
  `Import` reported `ErrRolledBack` — "your existing database is unchanged and
  still in use" — while serving zero forms and zero users, so nobody could log in
  to notice. Now stat-checked, and the `ErrUnavailable` message names the parked
  file and warns that restarting re-enables the default admin login.
- **The handler's reassuring message was the `default` branch**, against AGENT.md
  §4. `ErrRejected` is named; `default` now claims nothing.
- **Test defects.** `TestImportRollsBackWhenTheSwapFails` did not test the swap —
  it blocked the park path, so the branch that actually puts the database back
  had no coverage, and reverting the swap fix verbatim passed the suite. The
  operator-message test asserted `Contains(msg, "unchanged")`, which two of the
  three messages satisfy. The EXDEV fix had no test at all, and could be reverted
  green. All closed, each verified by applying the regression.

The data race in `Reopen` was fixed rather than deferred, because the `/healthz`
probe turned it from a coincidence into a read every few seconds. All store
access now goes through a locked `conn()`.

`/healthz` is wired into the Dockerfile as a `HEALTHCHECK`. Without it the
endpoint was a route nobody called — `restart: unless-stopped` does not restart a
container whose process is alive and failing every request.

## Deferred items

**None.** Everything recorded across six review passes is either done or closed
below with a reason. The list ran 19 items at its longest.

## Closed without doing — with the reasoning

The deferred list is empty. These three were on it and are not being done; that
is a decision, not an omission, and the reasoning is here so it can be argued
with rather than rediscovered.

**Retyping `screen.Rule.Kind` / `.Type` as defined types.** Measured before
deciding: 163 uses of the constants, 44 field accesses, 47 `AddFilterRule` sites,
across four packages and their tests. The benefit is that `Rule{Type: "emial"}`
becomes a compile error instead of a silently inert rule. That state is already
unreachable from storage — the `filter_rules` CHECK constraint permits only the
five types and two kinds — and now unreachable in effect too:
`TestUnvalidatedValuesNeverOverMatch` proves that a rule with any unvalidated
kind or type value matches nothing rather than something extra, and
`TestMatchLogsOnlyForAGenuinelyUnknownType` pins the log on the fail-open branch.
So the change is ~250 mechanical edits for a guarantee three other things already
provide. Worth doing on a quiet day, in its own branch, with nothing else in
flight. Not worth doing at the end of a long session.

**Splitting `AdminStore` (20) and `QuarantineStore` (14).** The interface split
on its own buys nothing: giving `QuarantineHandler` two fields instead of one
leaves it reaching the same fourteen methods. The narrowing only becomes real if
the *handlers* split — `/admin/quarantine` and `/admin/rules` into separate
types, and `AdminHandler`'s forms half from its submissions half, which share
only `GetForm`. That is a feature-sized refactor with a design question in it
(what owns the shared shell), not a loose end. The interfaces make the seam
visible, which was the point of naming them.

**A "resend notification" action for restored submissions.** `notified = 0` on a
restored row records a notification that was never sent, and nothing reads the
column — no sweep, no retry, no admin action. Closing it properly means deciding
what the operator sees and does, which is product design rather than debt.

## Seventh pass — running every feature

Not a review: the product started with a real SMTP sink and webhook receiver
wired up, and every feature exercised through HTTP and the CLI. Two live bugs
came out of it, neither of which any amount of reading had found.

**An account could be created with no password at all.** Nothing enforced a
length on any of the four paths that set one. Verified against a running
instance: user "empty" with password "", HTTP 302 and the account created; then
logged in with an empty password and got 200 on /admin and /admin/users, and
created another user from that session. Reachable by accident — a form submitted
with the field blank, or a deploy script with an unset variable. Now enforced in
the store, where all four paths converge; the CLI needed no change because it
already prints the store's error. Not enforced on login, so an upgraded instance
does not lock anyone out.

**An unrecognised subcommand started the server.** The switch over os.Args had no
default, so `dsforms --help` served, and so would any typo. AGENT.md §4 already
states the rule — in a switch over a closed set the default denies — written
about handler code and true here unchanged.

**Three things I misread as bugs before checking.** Worth recording because the
pattern cost more time than the real findings. A batch of submissions was held
that should not have been; the reason was the repeat-IP tracker, since every
request came from 127.0.0.1 — the fix was a distinct X-Forwarded-For per
submission. Search "found nothing" because the result row renders the sender's
*name* and I grepped for the email. And url_in_name, gibberish and a custom
keyword all appeared not to score, because they weigh 4, 3 and 5 against a
threshold of 6 — confirmed firing by dropping the form's threshold to 1, which
showed exactly those weights. In each case the product was right and the test
was wrong.

## Eighth pass — driving the UI in a real browser

A full pass through the admin UI in Chrome against a freshly built image and an
empty database: login, forms, the generated snippet, a cross-origin submission
from a customer page, the drawer, bulk select and delete, search, quarantine,
filter rules, waitlists, account, users, backups including a real upload-restore
round trip, the 404 page, the sidebar toggle and logout. No console errors.

Two things with bad histories here both held up: the drawer closes on **one**
press of Back, and a restore through the file upload swapped the database,
reverted post-snapshot data, left no `.rollback` behind and kept the container
healthy.

Four defects came out of it, none visible from the test suite. Each needed a
browser or a probe against a running instance.

**Landed: the password minimum the UI has always promised.** `account.html` and
`users_new.html` said "minimum 12 characters"; the store enforced 8. The Nocturne
port wrote the markup a day before any minimum existed in code, and when one
arrived it was a different number — so the two were never in agreement. Raised
the check to 12 and removed the number from the markup: both hints call
`minPassword`, and the strength meter in `static/app.js` now reads
`data-strength-min` instead of keeping its own copy of 12. Prerequisite commit
collapsed five `template.FuncMap` literals into `handler.TemplateFuncs()`.

**Landed: the database size counts the write-ahead log.** The card read "4.0 KB"
while the main file was 4,096 bytes and the WAL held 2,084,752. It reports disk
footprint now, which is an upper bound: SQLite's automatic checkpoint reuses the
WAL rather than truncating it, so the file stays at its high-water mark
(measured — every frame checkpointed, file unchanged at 4,165,352 bytes). Over-
reporting disk use is the mild error; under-reporting is the one that tells an
operator their submissions are gone. Also fixed a panic in `humanBytes` past a
terabyte — an unguarded index in a function that runs on every page render.

Review caught the same shape as the previous branch: every test stopped at
`dbStatus`, so hardcoding the status in `Shell` left the fix dead with the suite
green. There is a rendered-page test now. It also caught that my justification
for excluding `-shm` ("a fixed size would dominate a small database") applies to
the WAL itself at 128x the scale — the fix was right, the reasoning was not.

**Landed: the open redirect is closed.** `_redirect` was returned verbatim in a
Location header. The rule now is that the operator-configured `Redirect` is the
trust anchor — a submitter picks a page, within an origin an operator vouched
for — plus relative paths and `BASE_URL`. New leaf package `internal/urlsafe`,
which also absorbed the webhook check that was inlined twice. Accepted upgrade
break: a form with no configured Redirect on an instance with no `BASE_URL` now
sends visitors to `/success` instead of an absolute `_redirect`.

The first version of that fix did not work, and review is what found it:
`/../\evil.example.net` passed the check and reached the browser as
`/\evil.example.net`, because `net/http` runs `path.Clean` over a relative
Location and that promotes the backslash to the front. The guard was validating
the string that arrived rather than the string that would be sent. Writing the
regression as a property over the *emitted* form then found a second bypass
review had cleared as safe — `/%5c%5cevil.example.net`, where `url.Parse`
decodes the escape into a backslash. And getting the property right took one
more correction: both the first attempt and the review's suggested fix used
`EscapedPath()`, which renders a backslash as `%5C`, so the check could not see
the character it existed for and passed against the live bug. `http.Redirect`
cleans `u.Path`. Measured against a real Location header, then pinned.

Second hole from the same review: the *fallback* was never validated. A row
stored before the write-side check existed — nothing backfilled them — was
handed to the browser verbatim, including on the path where a hostile
`_redirect` had just been refused.

**Landed: accepted submissions no longer report a score they never had.** Score
and threshold were computed for every submission and persisted only on the held
path, so the drawer rendered the column default. They are written from the
struct now — `CreateSubmission` has one production caller and a long tail of
fixtures, so a signature change would have been almost pure churn. Signals are
still not stored for accepted rows: the reader tells "was held, then restored"
from "passed" by whether any exist.

Review made this branch twice as long as the fix. Three mutations survived the
first version, each one a distinction a comment claimed to be making and nothing
asserted — the clamped-versus-configured threshold, the package default, and the
signals decision itself. And adding a third panel branch introduced a worse
defect than the one it fixed: a restored submission whose breakdown could not be
*read* fell through to "Scored below the threshold, so it was delivered",
printed beside a notice saying it had been held, above a score above the
threshold. The panel has five states now and the test lists, for each, both what
it must say and what it must not.

**This fix is not retroactive.** Submissions received before it have no stored
score and none can be recovered, so they say so rather than claiming zero.

## Open questions from this pass

- The daily digest is still only unit-tested. It runs on a hardcoded 24-hour
  ticker with no way to trigger it, so nothing has watched one arrive.
- Browser-side JavaScript beyond the account page is exercised by tests only
  through rendered HTML, not by driving it.


## What the review rounds caught, and what it says about guards

Worth recording because the pattern repeats. The branch's tests were green, and
the review found the branch did not assert its own central claim:

- **`minPassword` could return `MinPasswordLength - 5` with the whole suite
  green.** Two structural guards — no literal in the markup, function registered
  in the map — and neither rendered a page, so neither could notice the number
  being wrong. Fixed by asserting against the *rendered output* of both real
  templates, which catches every wording because it checks the value. The
  mutation now fails on four assertions.
- **The first duplication guard was a string grep and was evadable four ways**
  (`make`, a conversion, a type alias, an aliased import). `internal/astcheck`
  exists in this repo *because* an aliased import defeated a matcher once
  already, and its package doc says so — writing a second string matcher after
  that was the mistake the package was extracted to prevent. Rewritten as an
  `astcheck.Detector` with all four evasions as fixtures.
- **Both handler-side length checks had no test at all**, and removing one is
  worse than a no-op: the store's `ErrPasswordTooShort` is not a UNIQUE error, so
  the handler falls through to a 500. A short password became "internal error"
  instead of a sentence. Both now tested, and the mutation fails with `status =
  500`, which is how the consequence was confirmed rather than assumed.
- **Four sibling tests were passing for the wrong reason** — they used passwords
  below the minimum and asserted only "an error appeared", so they depended on
  the *order* of checks in the handler rather than on reaching their own branch.
  All four now assert the specific sentence.
- **Three claims in my own comments and commit messages were false**, and git
  disproved each: the hint had not been there "since the templates were written"
  (one day, not five months); there was one full duplicate of the FuncMap, not
  two; and `TestCreateUserBcryptsPassword` was correct on `main` — I broke it
  while lengthening fixtures and then described fixing my own breakage as
  finding a pre-existing defect. Corrected in place. A comment that invents
  history is worse than no comment, because it is the version the next person
  believes.

## Accepted risks

| Risk | Why accepted |
|---|---|
| IP and CIDR **allow** rules trust `X-Forwarded-For` | `ExtractIP` has trusted the header unconditionally since before this work, and dsforms is designed to sit behind a reverse proxy that sets it. Allow rules make that a scoring bypass rather than only an attribution problem, so it is worth knowing: an IP allowlist is only safe behind a proxy you control. Email and domain allow rules are not affected — they match the sender field, which the submit handler validates. Re-architecting XFF trust needs a proxy-configuration decision and is out of scope here. |

## Open questions

_None currently — D1–D10 in the spec settle the handoff's open items._

## Ninth pass — the header laid itself out by accident

Reported from production on a phone: the search box and the "New form"
button were indented from the content edge, and the page title sat beside
the wordmark instead of on its own line.

Three defects, all in `.header-actions`, all from treating a wrapping flex
container as if it laid out in reading order.

1. **`margin-left: auto` is a spacer, not an alignment.** On one line it
   pushes the cluster right; once the cluster wraps to a line of its own it
   absorbs the leftover width as a *left* indent. Measured 27px at 390px —
   and 0px at 320px, where the cluster happened to fill the row exactly.
   That width-dependence is why it survived every earlier pass: whoever
   last checked it checked at a width where the bug is invisible.

2. **A shrinkable flex item contributes its min-content size, not its
   flex-basis, to its container's intrinsic width.** `.search`'s
   `flex: 0 1 230px` sized the cluster to 335px, under `230 + gap + button`,
   so the primary action wrapped under the search box and the header stood
   105px tall. This was live in v1.0.0 on every desktop width and nobody
   had reported it.

3. **A wrapping flex container wraps before it shrinks.** Fixing (2) left
   the wrap reachable just above the breakpoint: at 680px the cluster
   resolved to 453px against contents needing 456, and three pixels ejected
   a button while the search box kept all 230. `nowrap` makes the search
   absorb the shortfall; the header still wraps, so the escape valve is
   intact. Found by the review round, then confirmed by measurement.

The title block was a bare `<div style="min-width:0">` with no class, so no
rule could address it — the fix was unreachable before it was written.

**Method note, worth carrying.** The first three measurements this pass were
wrong: a leftover `/tmp/dsf-p1` process from the previous session held port
8096 and shadowed the container's published port, so `docker run` succeeded,
`curl` answered, and every number came from a stale binary. The fix appeared
to do nothing. Check *what is actually listening* before concluding a change
had no effect — `lsof -nP -iTCP:<port> -sTCP:LISTEN` settled it in one call.
This is the same lesson as the two earlier ones in this file: the thing that
looked like a code problem was a measurement problem.

## Open questions from the ninth pass

- The header is 162px tall at 390px and 205px on a two-button page at 320px.
  Correct, and no worse than before, but chunky. Hiding the `.crumb` eyebrow
  at mobile would reclaim ~18px. Not done: it is a design change, not a fix.
- No automated check covers layout. `TestResponsiveRulesTargetClassesThatExist`
  catches a rule targeting a class nothing carries — the silent half — but
  nothing catches a rule that applies and is simply wrong. Every defect here
  was found by measuring a running browser, which remains the only way.
