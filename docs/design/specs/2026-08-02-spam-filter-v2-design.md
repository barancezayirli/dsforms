# Spam Filter v2 Design

**Date:** 2026-08-02
**Branch:** `feature/spam-filter-v2`
**Status:** Approved, pending implementation

## Goal

Close the gap between the current `internal/spam` filter (link/markup/keyword scoring,
shipped 2026-06-25) and a batch of real spam pulled from production submissions
(`submissions.csv`, 51 rows, spanning 2026-07-03 to 2026-08-02) that the current filter
lets through untouched. None of these samples contain markup links, multiple URLs, or
existing keywords — the current filter has zero visibility into them by construction.

## Real samples driving this revision

Four distinct patterns in the leaked batch:

1. **Random-gibberish fill** — e.g. company `Ecthogb LLC`, message `AvAJQuWVbvzQPBGpngyOW`,
   name `Gfngfxr Viqymx`. ~8 rows. No links, no keywords.
2. **"RobertDon" price-bot** — name always `RobertDon`, company always `google`, sender
   rotates between two Gmail addresses, message is the same "what's your price" question
   machine-translated into a different language per submission. 8 rows, reusing a small
   pool of source IPs 3-4x each (e.g. `77.90.185.5` four times, `141.98.11.224` three
   times).
3. **Foreign-language message + one bare link** (Russian real-estate, Hebrew adult
   content). Single link only, scores 0 under "each link past the first."
4. **Copy-paste recruiter/outreach template** — identical English boilerplate, only the
   name substituted, sent twice.

Patterns 1 and 2 are addressed here. Patterns 3 and 4 are explicit known gaps (see Out of
scope).

Note: gibberish-token detection was **explicitly deferred** in the 2026-06-25 revision of
the original design as "the most false-positive-prone signal, add only if a future sample
evades" the existing checks. This revision is that trigger — pattern 1 evades every
existing signal.

## Decisions

- **Pattern 1 (gibberish) stays within the existing pure-function architecture.**
  `Score`/`IsSpam` remain stateless; add a new signal type.
- **Pattern 2 (repeat-identity bot) requires state**, because the signal — the same actor
  submitting to the same form repeatedly — is a fact about a *sequence* of submissions,
  not visible in any single one. This is new: `internal/spam` gains its first stateful
  component, deliberately modeled on `internal/ratelimit.Limiter` (in-process,
  mutex-guarded) rather than a new DB table, keeping the "no schema change" constraint
  from the original spec. Unlike `Limiter`, eviction is count-based (bounded LRU) rather
  than time-based, so no `now` injection is needed — see Storage below.
- **Dedup key is `formID + submitter IP`**, not form field values (e.g. `name`+`company`).
  dsforms hosts arbitrary third-party form schemas; IP is the one identity signal
  `submit.go` already extracts (`ExtractIP`) independent of any site's field names.
- **Conservatism carries forward unchanged:** a false positive is unrecoverable (silent
  drop, nothing stored), so every content-shape heuristic must stay a weak, pile-up-only
  signal. The IP-repeat check is the one exception — see below for why.

## Architecture

`internal/spam` gains one new file, `internal/spam/gibberish.go` (pure), and extends the
package with a stateful `Tracker` type (`internal/spam/tracker.go`), following the file
split already used by `internal/ratelimit` (`ratelimit.go` has both `Limiter` and
`LoginGuard`, kept in one file since the package is small — same rationale applies here).

```go
package spam

// Score / IsSpam — unchanged signatures, gibberish folded into Score.
func Score(data map[string]string) int
func IsSpam(data map[string]string) bool

// Tracker — new stateful component, constructed once in main.go and held on the
// handler struct, exactly like ratelimit.Limiter.
type Tracker struct { /* mutex-guarded bounded map */ }

func NewTracker(maxEntries int) *Tracker

// Seen records a submission from (formID, ip) and reports whether this occurrence
// is the 3rd or later from that pair (i.e. should be treated as spam).
func (t *Tracker) Seen(formID, ip string) bool
```

`submit.go` gains a `tracker *spam.Tracker` field on the handler struct (constructed in
`main.go` next to `limiter := ratelimit.NewLimiter(...)`), and the spam check becomes:

```go
repeated := h.tracker.Seen(formID, ip) // always call: it records too, not just reports
if spam.IsSpam(data) || repeated {
    respondSuccess(w, r, formID, redirectURL)
    return
}
```

`Seen` must be called unconditionally, not as the right side of `||` — Go's short-circuit
evaluation would skip it whenever `IsSpam(data)` is already true, silently undercounting
that IP's repeat tally for every submission that happened to be caught by content scoring
first. `Seen` is called after `ip := ExtractIP(r)` is computed (submit.go:103), so the
check order shifts slightly from the current code — content scoring first (cheap, no
lock), then the tracker (needs the IP, needs a lock).

## Detection signals — new in this revision

### A. Gibberish-field heuristic (stateless, folded into `Score`)

For every field value, split on whitespace into tokens. A token is **gibberish** if its
length is ≥6 **and** (vowel-ratio <20% **or** it has ≥3 letter-case transitions after the
first character — e.g. `AvAJQuWVbv...` flips case constantly; real words and names don't).
A field is flagged once if it contains any gibberish token (multiple gibberish tokens in
one field don't multiply the score).

```go
const gibberishWeight = 3 // half the threshold — pile-up only, never an instant drop
```

**Why weight 3, not higher:** vowel-ratio is a real-word heuristic biased toward
English/Romance phonotactics. Tested against this branch's own recruiter-spam sample —
`Sobczyk` (a real Polish surname, `Mateusz Sobczyk` row) scores a 14% vowel ratio and
trips the same rule as the random-string rows. A single flagged field must never drop a
submission alone, or dsforms silently loses real inquiries from consonant-heavy-language
names. Two independently-flagged fields (as seen in every gibberish sample: message *and*
name both flag on their own) reliably clears the threshold; one flagged field, alone,
sits at 3 — kept.

Verified against the CSV: the `Ecthogb LLC` / `AvAJQuWVbvzQPBGpngyOW` / `Gfngfxr Viqymx`
row scores 6 from message + name alone (company's `Ecthogb`, at a 29% vowel ratio and no
case transitions, doesn't even need to flag). The Sobczyk row scores 3 total — kept.

### B. IP-repeat tracker (stateful, separate from `Score`)

Key: `formID + ip` (IP from `ExtractIP`, matching the existing rate limiter's identity
signal). Rule: the **3rd and later** submission from the same `(formID, ip)` pair is
treated as spam. Not the 2nd — a real visitor resubmitting once (typo, follow-up message)
must never be dropped; the RobertDon sample reuses each IP 3-4 times, so the 3rd+
threshold still catches it while adding a margin against legitimate repeats.

This is the one instant-drop, non-pile-up signal in this revision: unlike content shape,
"this exact actor has hit this exact form 3+ times" is a fact about behavior, not a fuzzy
inference, so it doesn't need the conservatism budget content heuristics do.

**Storage:** bounded LRU (max entries, e.g. 10,000; evict oldest on overflow), not
TTL-based. In-process only — resets on redeploy/restart. This is an accepted tradeoff
(flagged explicitly, not silently): it will not catch a slow-drip campaign that resumes
after a restart days later. A durable version would need a DB table, which the original
spec deliberately avoided and this revision preserves.

**Shared-IP / NAT tradeoff (accepted, not solved):** the repeat count is permanent for the
process lifetime — it never decays, and the 3rd+ rule is an instant drop rather than a
pile-up signal. Behind a shared or NAT'd address (corporate office, university, co-working
space, carrier-grade mobile NAT), every visitor presents the same IP, so the 3rd distinct
*legitimate* person to ever contact a site from that address is silently dropped, as is
everyone after them, until the process restarts. This is the one place in the filter where
a false positive is not pile-up-gated, and it is accepted knowingly: the alternative
signals (form field values) are unavailable given arbitrary third-party form schemas, and
the realistic dsforms deployment — a low-traffic static site — sees far more bot repeats
from one IP than three unrelated humans behind one NAT. Mitigations deliberately deferred:
a decay/TTL window (would reintroduce the `now` injection and time-based eviction this
revision removed), and a per-form allowlist for known office IPs (config surface the
original spec ruled out). If real reports of missed submissions from shared IPs appear,
adding decay is the first change to make.

## Testing (TDD)

- `internal/spam/gibberish_test.go` written first: table-driven cases for the
  token-gibberish predicate (vowel-ratio and case-transition branches independently),
  plus the false-positive guard cases (`Sobczyk`, `Ecthogb`, `Dropbox`-style vowel-light
  real brand names) proving they score below threshold alone.
- `internal/spam/spam_test.go`: extend `TestScore`/`TestIsSpam` with the gibberish
  pile-up cases; add the 2026-08-02 real samples (gibberish rows) to
  `TestRealSpamSamples`, pinned the same way as the 2026-06-25 batch.
- `internal/spam/tracker_test.go` written first: table-driven — 1st/2nd calls for a pair
  return `false`, 3rd+ returns `true`; different `(formID, ip)` pairs don't interfere; LRU
  eviction confirmed by inserting `maxEntries+1` distinct pairs and asserting the oldest
  is forgotten (no time dependency, so no `time.Sleep` needed either way).
- `submit_test.go`: a case asserting the 3rd submission from the same IP to the same form
  is dropped (mirrors the existing spam-drop assertion pattern: success response, zero
  rows in `Store`), and that a 2nd submission is still stored.
- `go test -race ./...` green before commit.

## Out of scope

- **Foreign-language message + single bare link** (pattern 3). The only generalizable
  signal available is "message is mostly non-Latin script," which risks silently dropping
  real non-English-speaking customers — not an acceptable trade for dsforms's audience.
- **Recruiter/outreach near-duplicate template** (pattern 4). The message text differs
  per submission (name substituted mid-body), so exact-hash dedup won't catch it; doing so
  needs fuzzy/shingled text similarity — real added complexity for one observed campaign.
  Revisit if more samples of this shape show up.
- **Persisting the IP tracker across restarts.** Would require a DB table; deliberately
  deferred (see Storage above).
- Admin "spam folder" UI, env-tunable weights — still out of scope per the original spec,
  unchanged by this revision.
