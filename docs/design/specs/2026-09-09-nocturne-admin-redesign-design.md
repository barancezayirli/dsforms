# Nocturne Admin Redesign

Date: 2026-09-09
Branch: `feat/nocturne-redesign`
Source: `reference/design_handoff_dsforms_admin/` (Claude Design handoff, synced 2026-09-09T18:13:22Z)

---

## Goal

Replace the current admin UI (50px navy top bar, 960px single column, three flat
stat counters) with a left-sidebar dashboard shell on the **Nocturne** design
system, and — in the same pass — turn the silent spam drop into a reviewable
30-day quarantine with a per-signal score breakdown.

The visual work is the visible half. The half that makes it possible is in
`internal/spam`: today `Score` returns a bare `int`, so nothing in the system can
say *why* a submission was held. The quarantine screen is not implementable
without changing that.

---

## Decisions

These were open in the handoff and are settled here.

| # | Decision | Rationale |
|---|---|---|
| D1 | **One feature branch**, `feat/nocturne-redesign`, one PR | Operator's call. The admin is half-migrated for the duration; phases below keep `go test ./...` green at every commit so the branch is always bisectable. |
| D2 | **Home = KPI grid + charts** (handoff §1 default), not the "Focused inbox" variation | The fuller dashboard. The `homeLayout` variation is not built. |
| D3 | **Vendor Inter + Phosphor via `//go:embed`** | Not merely a purity choice: the existing CSP (`main.go`) sets `default-src 'self'` with no `font-src`, so a Google Fonts `@import` is **blocked by our own header**. Vendoring is the only option that works without loosening CSP. |
| D4 | **All four optional extras in scope**: ⌘K search, digest email, false-positive reporting, rate-limit IP panel | Operator's call. See D5–D8 for how each is resolved. |
| D5 | **Search uses SQLite FTS5**, not `LIKE` | Verified available in `modernc.org/sqlite v1.47.0` under `CGO_ENABLED=0` (scratch program: `CREATE VIRTUAL TABLE … USING fts5` + `MATCH` both succeed). External-content table over `submissions`, kept in sync by triggers. `LIKE` over the JSON blob is the documented fallback if the triggers prove fragile. |
| D6 | **Rate-limit panel reads an in-process snapshot**, no `ip_activity` table | Persisting a row per request would put a SQLite write on the hot path the in-process limiter exists to avoid. `ratelimit.Limiter` grows a read-only `Snapshot()`; the UI copy says **"since last restart"** so it never implies persistence that does not exist. Same treatment for `spam.Tracker`. |
| D7 | **Spam weights stay absolute constants**; the threshold-invariant tradeoff is stated in the UI, not engineered away | Deriving weights from the threshold would change the meaning of already-stored `spam_signals` rows whenever an operator moves the slider — history must not be rewritten. See "The threshold invariant" below. |
| D8 | **Digest email is a daily `time.Ticker` goroutine** in `main.go`, mirroring the existing session-cleanup loop | Off by default, per-form toggle. The only background timer this redesign adds. |
| D9 | `Detail` **sorts field keys** before scoring | Go map iteration is randomized. `Score`'s `int` return hid this; returning an ordered `[]Signal` exposes it, and the handoff asks for a test asserting signal order. |
| D10 | The `youruser` → `barancezayirli` module-path mismatch is **not** fixed here | `go.mod` says `github.com/youruser/dsforms`; `CLAUDE.md` documents `github.com/barancezayirli/dsforms`. Renaming touches every import in the repo and would bury this diff. Deferred — tracked in `SESSION_PROGRESS.md`. |

---

## The threshold invariant (D7, expanded)

`internal/spam/spam.go` currently encodes two invariants **in the type system**:

```go
const threshold = 6
const markupWeight = threshold  // one markup link drops on its own
const keywordWeight = 5         // below threshold: one keyword never drops alone
```

Making the threshold configurable breaks both at the extremes:

- At **Strict · 4**, a lone keyword hit (+5) now holds a submission by itself.
- At **Lenient · 9**, a single link-markup hit (+6) no longer holds one.

Resolution:

1. `markupWeight` becomes an absolute `6` with a comment reading *"equals the
   **default** threshold"* — the code must stop claiming an invariant that
   configuration can violate.
2. `DefaultThreshold = 6` is exported; `threshold` as a private const is retired.
3. The sensitivity `.seg` carries hint text naming the consequence of each
   preset, so the operator moving the slider sees what they are trading:
   - Lenient · 9 — "A single link-markup hit no longer holds on its own."
   - Balanced · 6 — "Default. One markup link holds; one keyword never does."
   - Strict · 4 — "A single keyword hit now holds a submission on its own."

Weights are **not** derived from the threshold. A stored `spam_signals` row is a
historical record of why a specific submission was held; a later config change
must not retroactively change what that row says.

---

## Architecture

### `internal/spam` — signal-returning entry point

```go
// Signal is one rule hit that contributed to a submission's score.
type Signal struct {
    Rule   string // "markup" | "keyword" | "sql" | "gibberish" | "url_in_name" | "extra_links" | "rule"
    Field  string // form field whose value matched ("" for whole-submission rules)
    Match  string // matched substring, truncated to 200 runes
    Weight int    // points this hit contributed
}

// Detail scores a submission and reports every rule hit that contributed.
func Detail(data map[string]string) (score int, signals []Signal)

func Score(data map[string]string) int   { s, _ := Detail(data); return s }
func IsSpam(data map[string]string) bool { return Score(data) >= DefaultThreshold }
```

Constraints:

- Field keys are **sorted** before iteration (D9). Within a field, rules are
  emitted in a fixed order: markup → sql → keyword → url_in_name → gibberish.
  `extra_links` is appended last, as a single whole-submission signal with
  `Field: ""` and `Weight: 2*(links-1)`.
- Weight constants remain the single source of truth; each `Signal.Weight` is
  stamped from them so a constant change flows to the UI without template edits.
- Gibberish is captured from the **original-case** value (the existing comment
  explains why lowercasing destroys the case-transition signal).
- `Match` is attacker-controlled. Truncate, store, and render through
  `html/template`'s default escaping. **Never** `template.HTML`.
- `Score` and `IsSpam` keep their exact current signatures, so every existing
  caller and test compiles unchanged.

### Schema (additive, `CREATE TABLE IF NOT EXISTS` on startup)

```sql
-- submissions: new columns. No column in this table is nullable, so "unset"
-- is expressed as a zero value rather than NULL, matching the existing style.
ALTER TABLE submissions ADD COLUMN is_held        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN spam_score     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN held_threshold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN held_at        DATETIME NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN notified       INTEGER NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS spam_signals (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  -- TEXT, not INTEGER: submissions.id is a UUID string. An INTEGER foreign key
  -- here would parse fine and never match a row.
  submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
  rule          TEXT NOT NULL,
  field         TEXT NOT NULL DEFAULT '',
  -- Not named "match": MATCH is a SQLite operator.
  match_text    TEXT NOT NULL DEFAULT '',
  weight        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS filter_rules (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,   -- 'block' | 'allow'
  type       TEXT NOT NULL,   -- 'email' | 'domain' | 'ip' | 'cidr' | 'keyword'
  value      TEXT NOT NULL,
  hits       INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL,
  UNIQUE(kind, type, value)
);

-- forms: per-form sensitivity override
ALTER TABLE forms ADD COLUMN spam_threshold INTEGER NULL; -- NULL = inherit instance default

CREATE INDEX IF NOT EXISTS idx_submissions_created  ON submissions(created_at);
CREATE INDEX IF NOT EXISTS idx_submissions_form_held ON submissions(form_id, is_held);
CREATE INDEX IF NOT EXISTS idx_submissions_read     ON submissions(read);
CREATE INDEX IF NOT EXISTS idx_spam_signals_sub     ON spam_signals(submission_id);
```

Column additions go into the existing `runAlterMigrations`
(`internal/store/store.go`), which already runs `ALTER TABLE … ADD COLUMN` and
swallows the `"duplicate column"` error on every startup. Each new column is
*also* declared in the `CREATE TABLE` above, so a fresh database gets it from
the schema and the ALTER is a no-op — the same double declaration the repo
already uses for `forms.webhook_url`. No `PRAGMA table_info` guard is needed,
and no version table exists.

Indexes over the new columns cannot live in the schema constant: that runs
before the ALTERs, so on an upgrade from a pre-quarantine database the column
would not exist yet. They run after the column pass instead.

`held_threshold` is stored per-submission (beyond the handoff's list) so the
quarantine meter's "threshold N" label reflects what was applied at hold time,
not today's setting.

`notified` defaults to **1**, not 0: an ordinary accepted submission has already
had its notification sent by the time the row settles, and only a held row is
written with 0 to record that one is still owed.

### Submit-handler evaluation order

```
honeypot filled   → drop (unchanged, and deliberately still first)
allow rule match  → accept immediately, skip scoring
block rule match  → hold; spam_score = effective threshold; one signal, rule='rule'
Detail(data)      → score >= effectiveThreshold ? hold : accept
```

The handoff specifies `allow → block → honeypot → score`. Honeypot stays first
instead: the allow list matches on the attacker-supplied `email` field, so
checking it before the honeypot would let a bot bypass the honeypot entirely by
spoofing an allowlisted address.

`spam.Tracker` repeat-IP hits also drop silently today
(`contentSpam || repeated`). They become held submissions carrying a
`repeat_ip` signal, or the quarantine still loses submissions invisibly.

`effectiveThreshold = form.SpamThreshold ?? config.SpamThreshold ?? spam.DefaultThreshold`.

Held submissions are stored with `notified = 0` so `RestoreSubmission` can send
the withheld notification. `hits` increments on every rule match. Custom keywords
(`kind='block', type='keyword'`) feed the keyword pass at +5 rather than
short-circuiting — preserving the existing rule that one keyword never holds a
message alone.

### Store methods (all in `internal/store/store.go`)

Quarantine: `HeldSubmissions(page, size)`, `HeldCount(since)`, `SubmissionSignals(id)`,
`RestoreSubmission(id)`, `DeleteHeld(ids)`, `PurgeHeldOlderThan(d)`.

Filter rules: `ListFilterRules(kind)`, `AddFilterRule(kind, type, value)`,
`DeleteFilterRule(id)`, `MatchFilterRules(data, ip)`, `IncrementRuleHits(id)`.

Aggregates: `SubmissionsPerDay(days)`, `UnreadCount()`, `PerFormStats()`,
`RecentSubmissions(n)`, `WaitlistGrowth(days)`, `TopSpamSignals(days)`.

Search: `SearchSubmissions(q, limit)` over the FTS5 index.

### Config

```go
SpamThreshold int // SPAM_THRESHOLD, default 6, clamped 1..20 (0 or unset ⇒ default)
```

Documented in `README.md`'s configuration table and `.env.example`.

### Assets

- **Inter** 400/500/600/700, woff2 subset, `//go:embed static/fonts/*`, served from
  `/static/` with a long `Cache-Control` and `@font-face` in `base.html`.
- **Phosphor** regular — the ~45 named icons inlined as a single SVG
  `<symbol>` sprite embedded in `base.html`, referenced by `<use href="#ph-x">`.
  No icon font, no CDN, no extra request.

Both are MIT/OFL. Licence files vendored alongside.

---

## Build order

Each phase ends with `go test -race ./...` green and a commit.

| Phase | Content |
|---|---|
| 0 | Vendored assets, Nocturne tokens, `base.html` shell rewrite (sidebar, header, fading rules, component classes) |
| 1 | `spam.Detail` + `Signal` + `DefaultThreshold`; `config.SpamThreshold`; schema migrations; store methods |
| 2 | Submit-handler rewiring (allow → block → honeypot → score → hold) |
| 3 | Forms, form detail, reader drawer |
| 4 | Quarantine + score breakdown; Filter rules |
| 5 | Home aggregates, inline-SVG charts, rate-limit snapshot panel |
| 6 | Form settings, Waitlists, Users, Backups, Login, empty states |
| 7 | FTS5 search (⌘K), digest email, false-positive reporting |
| 8 | Landing page (`docs/index.html`) |

---

## Testing (TDD, per `CLAUDE.md`)

- `spam.Detail`: table-driven, one case per rule asserting both score and the
  exact `[]Signal` (rule, field, weight); a multi-signal case asserting **order**;
  a clean-submission case asserting `nil` signals; a case asserting `Match` is
  truncated at 200 runes.
- `Score`/`IsSpam` wrapper tests confirm the existing behaviour is unchanged —
  the current `spam_test.go` (375 lines) must pass untouched.
- Store: every new method gets coverage, including `RestoreSubmission` clearing
  `is_held`, marking unread, and leaving `notified = 0` for the notifier.
- Submit handler: allow-beats-block, block-beats-honeypot, per-form threshold
  override, and that a held submission sends **no** notification.
- Filter rules: validation per type (email, hostname, IPv4/IPv6, CIDR), duplicate
  rejection, overlapping-CIDR rejection.
- `PurgeHeldOlderThan` uses injected time — no `time.Sleep` (rule 9).

## Out of scope

- The "Focused inbox" home variation (D2).
- Module-path rename (D10).
- Persisted rate-limit history (D6) — snapshot only.
- Any charting library, JS framework, or build step.
