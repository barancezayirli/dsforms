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
| Markup link (`[url=`, `[link]`, `<a href`) | Almost never legitimate in a plain static-site form | +5 each |
| High-confidence keyword hit (small baked-in list: casino / pharma / SEO-backlink / crypto-pump terms) | Direct content-spam signal | +5 each |
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
