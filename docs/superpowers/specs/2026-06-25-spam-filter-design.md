# Spam Filter Design

**Date:** 2026-06-25
**Branch:** `feature/spam-filter`
**Status:** Approved, pending implementation

## Goal

Add a third anti-spam layer to form submissions that judges submission **content**,
targeting link/content spam that gets past the existing honeypot (`_honeypot`) and
per-form rate limiter. No external API, no new dependencies, no JS.

## Decisions

- **Detection technique:** weighted heuristic scoring (not hard pass/fail rules, not a
  learned/Bayesian filter). A learned filter is ruled out because spam is dropped
  silently — there is no stored corpus or "mark as spam" feedback loop to train on.
- **Behavior on match:** silent drop. Mirror the honeypot exactly — return a successful
  response (JSON `{"success": true}` or redirect) and store nothing. No DB schema change.
- **Configuration:** fully hardcoded. No new env vars. Weights and threshold live in the
  `spam` package. Filter is always on.
- **Conservatism:** because a false positive permanently loses a real message, the
  threshold is deliberately high enough that a single weak signal never drops a
  submission. Only a pile-up of signals trips it.

## Architecture

New pure package `internal/spam/` (stdlib only, no state, no DB, no config struct),
mirroring `internal/ratelimit/` as a self-contained unit.

```go
package spam

// Score returns a non-negative spam score for a submission's field values.
func Score(data map[string]string) int

// IsSpam reports whether data scores at or above the drop threshold.
func IsSpam(data map[string]string) bool
```

`submit.go` calls `IsSpam`. `Score` is exported so tests assert exact point totals per
signal. The function is a deterministic function of the input map — the most testable
possible shape (table-driven input → expected score).

## Detection signals

All checks run over the **values** of the filtered `data` map (the same map that gets
stored), case-insensitively. Representative weights — exact numbers locked down in tests:

| Signal | Rationale | Weight |
|---|---|---|
| Each link past the 1st (`http://`, `https://`, `www.`) | Contact forms rarely need multiple URLs; bots stuff many | +2 each |
| Markup link (`[url=`, `[url]`, `[link]`, `<a href`) | Almost never legitimate in a plain static-site form | **+6 each (= threshold; instant drop — see Revision)** |
| High-confidence keyword hit (casino / pharma / SEO-backlink / crypto-pump / `unsubscribe` — see Revision) | Direct content-spam signal | +5 each |
| A URL inside a short "name"-like field | Names are not URLs | +4 |

The keyword list stays short and high-confidence on purpose — broad word lists are how
filters eat real messages.

### Threshold: 6

Worked examples demonstrating conservatism:

| Submission | Score | Outcome |
|---|---|---|
| Normal message, one link, no spam words | 0 | kept |
| Long enthusiastic message, two links | 2 | kept |
| One BBCode link, nothing else | 5 | kept (single weak signal alone) |
| "Cheap backlinks SEO service, visit `[url=...]`" | 10 | dropped |
| Three links + one pharma keyword | 9 | dropped |

## Integration

In `submit.go`, the spam check goes after `redirectURL` is determined
(`submit.go:99`) — so it can be passed to `respondSuccess` — and before the IP/DB
marshal work (`submit.go:100+`):

```go
if spam.IsSpam(data) {
    respondSuccess(w, r, redirectURL)   // mirror honeypot: look successful, store nothing
    return
}
```

### Targeted refactor

The honeypot block (`submit.go:75-82`) and the final success response
(`submit.go:140-147`) both duplicate the same "JSON success vs. redirect" logic. Extract
it into one helper and reuse it in all three spots (honeypot, spam, real success), so the
new branch does not add a fourth copy:

```go
func respondSuccess(w http.ResponseWriter, r *http.Request, redirectURL string)
```

## Testing (TDD)

- `internal/spam/spam_test.go` **written first**: table-driven cases for `Score` (each
  signal in isolation + combinations) and `IsSpam` boundary cases (score 5 → false,
  6 → true). Confirm the tests fail before implementing.
- `submit_test.go`: a new case asserting a spam payload returns success/redirect **and**
  that `Store` has zero submissions afterward (proving the silent drop). Plus a regression
  case that a normal multi-line message with one link still stores.
- `go test -race ./...` green before commit.

## Out of scope

- Admin "spam folder" UI (not needed for silent drop).
- Env-tunable weights/threshold (chosen: fully hardcoded).
- IP reputation, time-trap, or burst detection (existing rate limiter covers floods;
  honeypot covers dumb bots).

---

## Revision 2026-06-25 — real-sample-driven retune

After shipping the first version, real spam samples from production showed the original
threat model (multi-link / keyword pile-ups) was wrong. Three captured samples scored:

| Sample | Original score | Original outcome |
|---|---|---|
| AutoMisto24 — `<a href>` + the URL repeated bare | 7 | caught (only because the URL was duplicated) |
| Jayrn / marketersmentor — 3 links + gibberish + "Unsubscribe", no markup | 4 | **missed** |
| Russian proctology — single `<a href>`, punycode domain | 5 | **missed** |

Two of three slipped through. The shared, high-confidence signatures in the real spam are
**HTML/BBCode markup** (a near-certain bot signature in a plain form) and **bulk-email
leakage** ("Unsubscribe"). Retune:

1. **Markup link is an instant drop.** Markup weight raised from +5 to the threshold
   (`markupWeight = threshold`), so a single `<a href` / `[url=` / `[url]` / `[link]`
   occurrence alone crosses. Near-zero false positives — plain forms never contain markup.
2. **Add `"unsubscribe"` to the keyword list** (+5). A genuine contact message never
   carries an unsubscribe link; combined with the sample's links it clears the threshold.
   Conservatism preserved: "please unsubscribe me" alone scores 5 and is kept.
3. **Narrow `"forex"` → `"forex trading"` / `"forex signals"`** to remove the substring
   false positive flagged in review (e.g. "Foreximate").

Post-retune scores: AutoMisto24 → 8, Jayrn → 9, Russian → 6 — all dropped; a legit
one-link message stays at 0. All three real samples are baked in as regression tests.

**Still deferred (YAGNI):** gibberish-token detection and punycode/IDN flagging — present
in the samples but redundant (markup and unsubscribe+links already catch those), and
gibberish detection is the most false-positive-prone signal. Add only if a future sample
evades the above. The "4 plain links alone = 6 → dropped" edge from review is left as-is
(out of scope for this retune).
