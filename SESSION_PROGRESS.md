# Session Progress — Nocturne Admin Redesign

Branch: `feat/nocturne-redesign`
Spec: `docs/design/specs/2026-09-09-nocturne-admin-redesign-design.md`
Handoff: `reference/design_handoff_dsforms_admin/`

## Phases

| Phase | Content | Status |
|---|---|---|
| 0 | Vendored assets (Inter, Phosphor), Nocturne tokens, `base.html` shell | **done** |
| 1 | `spam.Detail`/`Signal`/`DefaultThreshold`, `config.SpamThreshold`, schema, store methods | **done** |
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
still open. `filter.SenderAddress` is now the single definition of "the sender",
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
including a `spam.Rule` "compile-time prompt" guarantee that Go does not
provide. `spam.AllRules` plus an AST-derived completeness test now builds the
guarantee that was previously only asserted.

## Accepted risks

| Risk | Why accepted |
|---|---|
| IP and CIDR **allow** rules trust `X-Forwarded-For` | `ExtractIP` has trusted the header unconditionally since before this work, and dsforms is designed to sit behind a reverse proxy that sets it. Allow rules make that a scoring bypass rather than only an attribution problem, so it is worth knowing: an IP allowlist is only safe behind a proxy you control. Email and domain allow rules are not affected — they match the sender field, which the submit handler validates. Re-architecting XFF trust needs a proxy-configuration decision and is out of scope here. |

## Deferred items

| Item | Why deferred | Target |
|---|---|---|
| Retyping `filter.Rule.Kind` / `.Type` as defined types | They are the "documented string set" AGENT.md §4 names, and `matches` fails open on an unknown type — an in-memory rule with a typo'd Type silently matches nothing, which for a *block* rule is a bypass rather than a no-op. The DB `CHECK` constraints make it unreachable today, so the invariant lives in SQLite rather than in Go. The constants already exist; the change is mechanical. | A follow-up PR |
| Deriving the SVG viewBox strings from the geometry constants | `SparkViewBox()` and friends return hand-written literals (`"0 0 220 40"`) while the paths are generated from `sparkWidth`/`sparkHeight`. Changing a constant updates every path and leaves the viewBox behind — the exact drift the function's own comment says it prevents. Three lines. | A follow-up PR |
| A "resend notification" action for restored submissions | `notified = 0` on a restored row records a notification that was never sent, but nothing reads the column — no sweep, no retry, no admin action — so a failed send is not retried. Needs UI design. The misleading comment claiming otherwise has been removed. | A follow-up PR |
| Wiring `docs/screenshots/` into the landing page | The five captures (dashboard, reader, submission, form-edit, login) are committed, but `docs/index.html` references none of them — the Admin UI section still describes the screens in cards instead of showing them. Only the page markup is outstanding. | A follow-up PR |

## Open questions

_None currently — D1–D10 in the spec settle the handoff's open items._
