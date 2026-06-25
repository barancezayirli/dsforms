# Spam Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a hardcoded, conservative weighted-scoring spam filter that silently drops link/content spam from form submissions.

**Architecture:** A new pure stdlib package `internal/spam` exposes `Score(map[string]string) int` and `IsSpam(map[string]string) bool`. The submit handler calls `IsSpam` on the filtered data map; on a match it mirrors the honeypot path — returns success and stores nothing. A small `respondSuccess` helper de-duplicates the three success-response sites in the handler.

**Tech Stack:** Go 1.25, standard library only (`strings`). No new dependencies.

## Global Constraints

- Module path: `github.com/youruser/dsforms`
- `CGO_ENABLED=0`; pure Go only
- Only these deps allowed: `github.com/go-chi/chi/v5`, `modernc.org/sqlite`, `golang.org/x/crypto`, `github.com/google/uuid` — this feature adds **none**
- No global state outside `main.go` (package-level `const`/`var` lookup tables in `spam` are fine — they are immutable config, not mutable state)
- All errors wrapped: `fmt.Errorf("context: %w", err)` (no error returns in this feature)
- No `panic`
- TDD: write the test file first, confirm it FAILS, then implement; every exported function tested; `go test -race ./...` must pass
- Test files live beside the file they test; use `t.Parallel()` where no mutable state is shared
- Spam filter is **fully hardcoded**: no new env vars, no `Config` changes
- On spam: **silent drop** — return a successful response, persist nothing

---

### Task 1: `internal/spam` scoring package

**Files:**
- Create: `internal/spam/spam.go`
- Test: `internal/spam/spam_test.go`

**Interfaces:**
- Consumes: nothing (pure stdlib)
- Produces:
  - `func Score(data map[string]string) int` — non-negative weighted spam score
  - `func IsSpam(data map[string]string) bool` — true when `Score(data) >= 6`

- [ ] **Step 1: Write the failing tests**

Create `internal/spam/spam_test.go`:

```go
package spam

import "testing"

func TestScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data map[string]string
		want int
	}{
		{
			name: "ham no links",
			data: map[string]string{"name": "Jane Doe", "message": "Hello, I loved your work."},
			want: 0,
		},
		{
			name: "ham one link",
			data: map[string]string{"message": "See https://example.com for details"},
			want: 0,
		},
		{
			name: "two links scores per link past first",
			data: map[string]string{"message": "https://a.com and https://b.com"},
			want: 2,
		},
		{
			name: "scheme plus www counts as one link",
			data: map[string]string{"message": "visit https://www.example.com"},
			want: 0,
		},
		{
			name: "markup link",
			data: map[string]string{"message": "[url=http://x.com]click[/url]"},
			want: 5,
		},
		{
			name: "single keyword",
			data: map[string]string{"message": "buy backlinks now"},
			want: 5,
		},
		{
			name: "two keywords",
			data: map[string]string{"message": "casino and forex deals"},
			want: 10,
		},
		{
			name: "three links plus keyword",
			data: map[string]string{"message": "forex http://a.com http://b.com http://c.com"},
			want: 9,
		},
		{
			name: "url in name field",
			data: map[string]string{"name": "http://spam.com", "message": "hi"},
			want: 4,
		},
		{
			name: "case insensitive keyword",
			data: map[string]string{"message": "Cheap BACKLINKS"},
			want: 5,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Score(tt.data); got != tt.want {
				t.Errorf("Score() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsSpam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data map[string]string
		want bool
	}{
		{
			name: "below threshold (score 5) kept",
			data: map[string]string{"message": "[url=http://x.com]click[/url]"},
			want: false,
		},
		{
			name: "at threshold (score 6) dropped",
			data: map[string]string{"message": "http://a.com http://b.com http://c.com http://d.com"},
			want: true,
		},
		{
			name: "well over threshold dropped",
			data: map[string]string{"message": "casino and forex deals"},
			want: true,
		},
		{
			name: "clean message kept",
			data: map[string]string{"name": "Jane", "message": "Loved the talk, thanks!"},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSpam(tt.data); got != tt.want {
				t.Errorf("IsSpam() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/spam/ -race -v`
Expected: FAIL — `undefined: Score` / `undefined: IsSpam` (package does not compile).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/spam/spam.go`:

```go
// Package spam provides a hardcoded, conservative weighted-scoring filter for
// detecting link/content spam in form submissions. It has no dependencies and
// no configuration: the weights and threshold are fixed in this file.
package spam

import "strings"

// threshold is the score at or above which a submission is treated as spam.
// Deliberately conservative: a single weak signal must not cross it, because
// callers drop matches silently and a false positive is unrecoverable.
const threshold = 6

// spamKeywords are high-confidence content-spam tokens, lowercased. Kept short
// on purpose — broad word lists are how filters eat legitimate messages.
var spamKeywords = []string{
	"casino",
	"viagra",
	"cialis",
	"backlinks",
	"seo service",
	"binary options",
	"forex",
	"crypto pump",
}

// markupLinkMarkers indicate HTML/BBCode link markup, which has near-zero
// legitimate use in a plain static-site form.
var markupLinkMarkers = []string{"[url=", "[url]", "[link]", "<a href"}

// nameFieldKeys are field names treated as "name-like"; a URL inside one is
// suspicious because names are not URLs.
var nameFieldKeys = map[string]bool{
	"name":      true,
	"fname":     true,
	"lname":     true,
	"firstname": true,
	"lastname":  true,
	"full_name": true,
}

// Score returns a non-negative weighted spam score for a submission's field
// values. Higher is spammier. All matching is case-insensitive.
func Score(data map[string]string) int {
	score := 0
	links := 0
	for key, value := range data {
		lower := strings.ToLower(value)

		// Count distinct links. "http" matches http:// and https://; "www."
		// catches scheme-less URLs; subtract "//www." so a scheme+www URL
		// (e.g. https://www.example.com) counts once, not twice.
		links += strings.Count(lower, "http") + strings.Count(lower, "www.") - strings.Count(lower, "//www.")

		// HTML/BBCode link markup.
		for _, marker := range markupLinkMarkers {
			score += 5 * strings.Count(lower, marker)
		}

		// High-confidence keyword hits.
		for _, kw := range spamKeywords {
			if strings.Contains(lower, kw) {
				score += 5
			}
		}

		// A URL inside a name-like field.
		if nameFieldKeys[strings.ToLower(key)] && containsLink(lower) {
			score += 4
		}
	}

	// Each link past the first.
	if links > 1 {
		score += 2 * (links - 1)
	}

	return score
}

// IsSpam reports whether data scores at or above the drop threshold.
func IsSpam(data map[string]string) bool {
	return Score(data) >= threshold
}

// containsLink reports whether an already-lowercased value contains a URL.
func containsLink(lower string) bool {
	return strings.Contains(lower, "http") || strings.Contains(lower, "www.")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/spam/ -race -v`
Expected: PASS — all `TestScore` and `TestIsSpam` subtests OK.

- [ ] **Step 5: Commit**

```bash
git add internal/spam/spam.go internal/spam/spam_test.go
git commit -m "feat: add spam scoring package"
```

---

### Task 2: Wire the filter into the submit handler

**Files:**
- Modify: `internal/handler/submit.go` (import; honeypot block ~75-83; success block ~140-147; add `respondSuccess`; add spam check after `redirectURL` is set ~99)
- Test: `internal/handler/submit_test.go` (add two cases)

**Interfaces:**
- Consumes: `spam.IsSpam(data map[string]string) bool` from Task 1
- Produces: `func respondSuccess(w http.ResponseWriter, r *http.Request, formID, redirectURL string)` — internal helper; the spam branch (silent drop) and the existing honeypot and normal-success paths all call it

- [ ] **Step 1: Write the failing tests**

Add to `internal/handler/submit_test.go` (the file already imports `net/http`, `net/http/httptest`, `net/url`, `strings`, and `store`, and defines `setupSubmit`):

```go
func TestSubmitSpamDropped(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{
		"name":    {"bot"},
		"message": {"casino and forex deals, buy backlinks"},
	}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Mirrors the honeypot: looks successful (redirect), stores nothing.
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (silent drop)", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (spam dropped)", len(subs))
	}
}

func TestSubmitHamWithOneLinkStored(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{
		"name":    {"Jane Doe"},
		"message": {"Loved the talk! Slides at https://example.com — thanks."},
	}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 1 {
		t.Errorf("submissions = %d, want 1 (ham stored)", len(subs))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/handler/ -race -run 'TestSubmitSpamDropped|TestSubmitHamWithOneLinkStored' -v`
Expected: `TestSubmitSpamDropped` FAILS — the spam payload is currently stored, so `submissions = 1, want 0`. (`TestSubmitHamWithOneLinkStored` may already pass; that is fine — it is a regression guard.)

- [ ] **Step 3: Add the `spam` import**

In `internal/handler/submit.go`, add to the import block (keep grouping/order consistent with the existing imports):

```go
	"github.com/youruser/dsforms/internal/spam"
	"github.com/youruser/dsforms/internal/store"
```

- [ ] **Step 4: Add the `respondSuccess` helper**

In `internal/handler/submit.go`, add this function (e.g. just below `Handle`, above `determineRedirect`):

```go
// respondSuccess writes a successful submission response — JSON if the client
// asked for it, otherwise a redirect. Shared by the honeypot, spam-drop, and
// normal-success paths so they cannot drift apart.
func respondSuccess(w http.ResponseWriter, r *http.Request, formID, redirectURL string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{"success": true}); err != nil {
			log.Printf("submit: form %s failed to write JSON response: %v", formID, err)
		}
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}
```

- [ ] **Step 5: Replace the honeypot block to use the helper**

Replace the existing honeypot block:

```go
	// Honeypot check — silently succeed without storing anything.
	if r.FormValue("_honeypot") != "" {
		redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}
```

with:

```go
	// Honeypot check — silently succeed without storing anything.
	if r.FormValue("_honeypot") != "" {
		respondSuccess(w, r, formID, determineRedirect(r.FormValue("_redirect"), form.Redirect))
		return
	}
```

- [ ] **Step 6: Add the spam check after `redirectURL` is set**

In `Handle`, find:

```go
	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
	ip := ExtractIP(r)
```

and insert the spam check between the two lines:

```go
	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)

	// Content spam — silently drop like the honeypot: look successful, store nothing.
	if spam.IsSpam(data) {
		respondSuccess(w, r, formID, redirectURL)
		return
	}

	ip := ExtractIP(r)
```

- [ ] **Step 7: Replace the final success block to use the helper**

Replace the trailing response block in `Handle`:

```go
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{"success": true}); err != nil {
			log.Printf("submit: form %s failed to write JSON response: %v", formID, err)
		}
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}
```

with:

```go
	respondSuccess(w, r, formID, redirectURL)
}
```

- [ ] **Step 8: Run the full handler test suite**

Run: `go test ./internal/handler/ -race -v`
Expected: PASS — including `TestSubmitSpamDropped`, `TestSubmitHamWithOneLinkStored`, and the existing `TestSubmitHoneypotIgnored` (proves the refactor preserved honeypot behavior).

- [ ] **Step 9: Run the whole suite with the race detector**

Run: `go test ./... -race`
Expected: PASS — no regressions anywhere.

- [ ] **Step 10: Commit**

```bash
git add internal/handler/submit.go internal/handler/submit_test.go
git commit -m "feat: drop content spam in submit handler"
```

---

## Self-Review

**Spec coverage:**
- Weighted scoring, not hard rules / not Bayesian → Task 1 `Score` ✓
- Silent drop, store nothing, mirror honeypot → Task 2 Step 6 + `TestSubmitSpamDropped` ✓
- Fully hardcoded, no env vars → no `Config`/`config.go` changes in either task ✓
- Conservative threshold (single weak signal kept) → `threshold = 6`; `IsSpam` boundary test score 5 → false ✓
- Signals: links past first, markup links, keyword list, URL in name field → all in `Score` ✓
- `respondSuccess` refactor reused by honeypot/spam/success → Task 2 Steps 4-7 ✓
- Tests written first, fail, then implement; `go test -race ./...` green → Task 1 Steps 1-2, Task 2 Steps 1-2, Step 9 ✓
- No new dependencies → stdlib `strings` only ✓

**Placeholder scan:** No TBD/TODO/"handle edge cases"; all code shown in full. ✓

**Type consistency:** `Score(map[string]string) int`, `IsSpam(map[string]string) bool`, and `respondSuccess(w, r, formID, redirectURL string)` are used identically everywhere they appear. ✓
