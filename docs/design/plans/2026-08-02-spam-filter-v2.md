# Spam Filter v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `internal/spam` to catch two real spam patterns the current markup/keyword filter has zero visibility into — random-gibberish field fill, and a bot that repeatedly resubmits the same form from the same IP.

**Architecture:** Two independent additions to `internal/spam`: (1) a pure gibberish-token predicate folded into the existing `Score` function as a new low-weight, pile-up-only signal, and (2) a new stateful `Tracker` type — modeled on `internal/ratelimit.Limiter`'s in-process, mutex-guarded pattern — that flags the 3rd+ submission from the same `(formID, ip)` pair. `submit.go` combines both into the existing silent-drop path.

**Tech Stack:** Go stdlib only (`strings`, `unicode`, `sync`) — no new dependencies, consistent with the rest of `internal/spam`.

## Global Constraints

- Module path: `github.com/youruser/dsforms` (verified in `go.mod` — differs from `CLAUDE.md`'s stated path; use the actual one).
- No ORM — this plan touches no SQL at all; `internal/spam` remains DB-free.
- No new dependencies.
- All errors wrapped `fmt.Errorf("...: %w", err)` — not applicable here (no fallible operations added).
- No `panic` except in `config.Load()` — **exception carried over from `internal/ratelimit`**: `NewTracker` panics on invalid `maxEntries`, exactly like `ratelimit.NewLimiter` panics on invalid `burst`/`perMinute`. This matches existing precedent in the codebase for constructor-time invariant violations, not a new pattern.
- Test files live next to the file they test; `t.Parallel()` in every test; table-driven where there are multiple input cases; `go test -race ./...` must stay green throughout.
- Every exported function/type gets at least one test; every error path gets a test (n/a here — no error paths added).

---

## File Structure

- **Create** `internal/spam/gibberish.go` — pure gibberish-token detection (`isGibberishToken`, `fieldHasGibberish`). No dependency on `spam.go`.
- **Create** `internal/spam/gibberish_test.go` — table-driven tests for the predicate, including the false-positive guard cases.
- **Modify** `internal/spam/spam.go` — add `gibberishWeight` const, fold `fieldHasGibberish` into `Score`.
- **Modify** `internal/spam/spam_test.go` — extend `TestScore`/`TestIsSpam` with pile-up cases, add one real sample to `TestRealSpamSamples`.
- **Create** `internal/spam/tracker.go` — new stateful `Tracker` type.
- **Create** `internal/spam/tracker_test.go` — table-driven-style tests for repeat counting and LRU eviction.
- **Modify** `internal/handler/submit.go` — add `Tracker *spam.Tracker` field, wire into `Handle`.
- **Modify** `internal/handler/submit_test.go` — add `Tracker` to every `SubmitHandler{...}` literal (5 sites), add two new IP-repeat tests.
- **Modify** `main.go` — construct `spam.NewTracker(10000)`, wire onto `submitHandler`.

---

### Task 1: Gibberish-token predicate (pure)

**Files:**
- Create: `internal/spam/gibberish.go`
- Test: `internal/spam/gibberish_test.go`

**Interfaces:**
- Produces: `isGibberishToken(token string) bool`, `fieldHasGibberish(value string) bool` — both unexported, consumed by Task 2's `Score`.

- [ ] **Step 1: Write the failing test**

Create `internal/spam/gibberish_test.go`:

```go
package spam

import "testing"

func TestIsGibberishToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "short token exempt regardless of shape", token: "LLC", want: false},
		{name: "short real word exempt", token: "IBM", want: false},
		{name: "random mixed-case string flagged", token: "AvAJQuWVbvzQPBGpngyOW", want: true},
		{name: "zero-vowel random string flagged", token: "Gfngfxr", want: true},
		{name: "low-vowel random string flagged", token: "Viqymx", want: true},
		{name: "real title-case word not flagged", token: "Ecthogb", want: false},
		{name: "real surname from consonant-heavy language not flagged alone", token: "Troskot", want: false},
		{name: "real word with normal vowel ratio not flagged", token: "Mateusz", want: false},
		{name: "vowel-light real brand name not flagged", token: "Dropbox", want: false},
		{name: "lowercase real word not flagged", token: "backlinks", want: false},
		{name: "ALLCAPS real word not flagged", token: "UNSUBSCRIBE", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isGibberishToken(tt.token); got != tt.want {
				t.Errorf("isGibberishToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestFieldHasGibberish(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "empty value", value: "", want: false},
		{name: "normal sentence", value: "Hello, I loved your work.", want: false},
		{name: "one gibberish token among real words", value: "Loved your Gfngfxr work", want: true},
		{name: "two-word gibberish name", value: "Gfngfxr Viqymx", want: true},
		{name: "single long gibberish token", value: "AvAJQuWVbvzQPBGpngyOW", want: true},
		{name: "real name with consonant-heavy surname", value: "Mateusz Sobczyk", want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := fieldHasGibberish(tt.value); got != tt.want {
				t.Errorf("fieldHasGibberish(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
```

Note: `"Mateusz Sobczyk"` is expected `true` here — `Sobczyk` alone trips the low-vowel-ratio rule (14% vowels). That's the known, accepted false-positive-prone case discussed in the design doc; Task 3's `spam_test.go` changes prove it stays *below the drop threshold* on its own. This test file only proves the token-level predicate's shape, not the scoring conservatism — that's Task 2/3's job.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/spam/... -run TestIsGibberishToken -v`
Expected: FAIL — `isGibberishToken`/`fieldHasGibberish` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `internal/spam/gibberish.go`:

```go
// Package spam — this file adds gibberish/synthetic-string detection, folded into
// Score by spam.go. Kept in its own file because it's a pure, self-contained
// predicate with no dependency on the rest of the scoring logic.
package spam

import (
	"strings"
	"unicode"
)

// gibberishTokenMinLen is the shortest token length the heuristic runs on. Below
// this, short real words and abbreviations (LLC, IBM, NASA) are statistically
// indistinguishable from noise, so they are left alone entirely.
const gibberishTokenMinLen = 6

// gibberishVowelRatioMax is the vowel-ratio floor below which a token is treated
// as synthetic. Chosen specifically below the ratio of known vowel-light real
// brand names (Dropbox 29%, Flickr/Tumblr ~17% — the latter still flag, which is
// why this signal stays low-weight and pile-up-only; see spam.go).
const gibberishVowelRatioMax = 0.20

// gibberishCaseTransitionsMin is the minimum number of letter-case flips (after
// the first character) that marks a token as synthetic. Real words/names are
// lowercase, Title Case, or ALLCAPS — they don't alternate case letter-by-letter
// the way generated IDs do (e.g. "AvAJQuWVbv...").
const gibberishCaseTransitionsMin = 3

// isGibberishToken reports whether a single whitespace-delimited token looks like
// a randomly generated string rather than real text. Must be called with the
// token's original casing preserved — lowercasing first would destroy the
// case-transition signal.
func isGibberishToken(token string) bool {
	if len(token) < gibberishTokenMinLen {
		return false
	}

	letters := 0
	vowels := 0
	transitions := 0
	prevWasUpper := false
	hasPrev := false

	for _, r := range token {
		isUpper := unicode.IsUpper(r)
		isLower := unicode.IsLower(r)
		if !isUpper && !isLower {
			continue // digits/punctuation don't count toward letter stats
		}
		letters++
		if strings.ContainsRune("aeiouAEIOU", r) {
			vowels++
		}
		if hasPrev && isUpper != prevWasUpper {
			transitions++
		}
		prevWasUpper = isUpper
		hasPrev = true
	}

	if letters == 0 {
		return false
	}
	if transitions >= gibberishCaseTransitionsMin {
		return true
	}
	return float64(vowels)/float64(letters) < gibberishVowelRatioMax
}

// fieldHasGibberish reports whether value contains at least one gibberish token.
func fieldHasGibberish(value string) bool {
	for _, token := range strings.Fields(value) {
		if isGibberishToken(token) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/spam/... -run 'TestIsGibberishToken|TestFieldHasGibberish' -v`
Expected: PASS, all subtests green.

- [ ] **Step 5: Commit**

```bash
git add internal/spam/gibberish.go internal/spam/gibberish_test.go
git commit -m "feat: add gibberish-token detection to spam package"
```

---

### Task 2: Fold gibberish detection into `Score`

**Files:**
- Modify: `internal/spam/spam.go`
- Modify: `internal/spam/spam_test.go`

**Interfaces:**
- Consumes: `fieldHasGibberish(value string) bool` from Task 1.
- Produces: no new exported symbols — `Score`/`IsSpam` signatures unchanged.

- [ ] **Step 1: Write the failing tests**

In `internal/spam/spam_test.go`, add these cases to the `tests` slice inside `TestScore` (after the existing `"url in mixed-case name field"` case):

```go
		{
			name: "single gibberish field alone kept (pile-up only)",
			data: map[string]string{"company": "Sobczyk Consulting"},
			want: 3,
		},
		{
			name: "two gibberish fields pile up to drop threshold",
			data: map[string]string{"message": "AvAJQuWVbvzQPBGpngyOW", "name": "Gfngfxr Viqymx"},
			want: 6,
		},
		{
			name: "short tokens exempt from gibberish check",
			data: map[string]string{"company": "IBM LLC"},
			want: 0,
		},
		{
			name: "vowel-light real brand name not flagged",
			data: map[string]string{"company": "Dropbox"},
			want: 0,
		},
```

Add these cases to the `tests` slice inside `TestIsSpam` (after the existing `"clean message kept"` case):

```go
		{
			name: "single gibberish field alone kept",
			data: map[string]string{"company": "Sobczyk Consulting"},
			want: false,
		},
		{
			name: "two gibberish fields at threshold dropped",
			data: map[string]string{"message": "AvAJQuWVbvzQPBGpngyOW", "name": "Gfngfxr Viqymx"},
			want: true,
		},
```

Add this case to the `tests` slice inside `TestRealSpamSamples` (after the existing `"russian proctology..."` case) — drawn from a real captured submission, the exact pattern this revision was written for:

```go
		{
			name: "gibberish-fill bot: random name/message, plausible-looking company/email",
			data: map[string]string{
				"company":      "Ecthogb LLC",
				"email":        "t.u.kilo.v.i.n.2.46@gmail.com",
				"inquiry_type": "Stack Audit",
				"message":      "AvAJQuWVbvzQPBGpngyOW",
				"name":         "Gfngfxr Viqymx",
				"saas_spend":   "Under $1,000/mo",
			},
			wantScore: 6,
		},
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/spam/... -run 'TestScore|TestIsSpam|TestRealSpamSamples' -v`
Expected: FAIL on the new cases — actual scores are all short of `want` by the gibberish contribution (e.g. "two gibberish fields..." gets 0, wants 6).

- [ ] **Step 3: Implement — fold gibberish into `Score`**

In `internal/spam/spam.go`, add the new weight constant after `keywordWeight`:

```go
// gibberishWeight is the score for a field whose value contains a token that
// looks synthetically generated rather than real text. Kept at half the
// threshold — pile-up only, never an instant drop — because the underlying
// vowel-ratio/case-transition heuristic is biased toward English/Romance-
// language phonotactics and can flag real names from consonant-heavy
// languages (e.g. "Sobczyk"). A single flagged field must never lose a real
// submission; two independently-flagged fields reliably means a bot.
const gibberishWeight = 3
```

In `Score`, add this block inside the `for key, value := range data` loop, after the "A URL inside a name-like field" block and before the loop's closing brace:

```go

		// Gibberish/synthetic-looking field value. Deliberately checked against
		// the original-case value, not lower — lowercasing would destroy the
		// case-transition signal fieldHasGibberish relies on.
		if fieldHasGibberish(value) {
			score += gibberishWeight
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/spam/... -v`
Expected: PASS — full package, including all pre-existing cases (no regressions).

- [ ] **Step 5: Commit**

```bash
git add internal/spam/spam.go internal/spam/spam_test.go
git commit -m "feat: fold gibberish-field detection into spam Score"
```

---

### Task 3: `Tracker` — bounded IP-repeat detector

**Files:**
- Create: `internal/spam/tracker.go`
- Test: `internal/spam/tracker_test.go`

**Interfaces:**
- Produces: `NewTracker(maxEntries int) *Tracker`, `(*Tracker).Seen(formID, ip string) bool` — consumed by Task 4's `submit.go` and `main.go`.

- [ ] **Step 1: Write the failing test**

Create `internal/spam/tracker_test.go`:

```go
package spam

import "testing"

func TestTrackerFirstTwoCallsNotSpam(t *testing.T) {
	t.Parallel()
	tr := NewTracker(100)
	if tr.Seen("form-a", "1.1.1.1") {
		t.Error("1st call = true, want false")
	}
	if tr.Seen("form-a", "1.1.1.1") {
		t.Error("2nd call = true, want false")
	}
}

func TestTrackerThirdCallIsSpam(t *testing.T) {
	t.Parallel()
	tr := NewTracker(100)
	tr.Seen("form-a", "1.1.1.1")
	tr.Seen("form-a", "1.1.1.1")
	if !tr.Seen("form-a", "1.1.1.1") {
		t.Error("3rd call = false, want true")
	}
}

func TestTrackerFourthCallStillSpam(t *testing.T) {
	t.Parallel()
	tr := NewTracker(100)
	tr.Seen("form-a", "1.1.1.1")
	tr.Seen("form-a", "1.1.1.1")
	tr.Seen("form-a", "1.1.1.1")
	if !tr.Seen("form-a", "1.1.1.1") {
		t.Error("4th call = false, want true")
	}
}

func TestTrackerDistinctFormsIndependent(t *testing.T) {
	t.Parallel()
	tr := NewTracker(100)
	tr.Seen("form-a", "1.1.1.1")
	tr.Seen("form-a", "1.1.1.1")
	if tr.Seen("form-b", "1.1.1.1") {
		t.Error("different form's 1st call = true, want false")
	}
}

func TestTrackerDistinctIPsIndependent(t *testing.T) {
	t.Parallel()
	tr := NewTracker(100)
	tr.Seen("form-a", "1.1.1.1")
	tr.Seen("form-a", "1.1.1.1")
	if tr.Seen("form-a", "2.2.2.2") {
		t.Error("different IP's 1st call = true, want false")
	}
}

func TestTrackerEvictsOldestOnOverflow(t *testing.T) {
	t.Parallel()
	tr := NewTracker(1)
	tr.Seen("form-a", "1.1.1.1") // count 1, inserted, at capacity
	tr.Seen("form-a", "2.2.2.2") // new key at capacity: evicts 1.1.1.1, count 1

	// 1.1.1.1 was evicted, so it needs 2 fresh calls again to prove it was
	// forgotten, not that it's continuing a prior count.
	if tr.Seen("form-a", "1.1.1.1") {
		t.Error("post-eviction 1st call = true, want false")
	}
	if tr.Seen("form-a", "1.1.1.1") {
		t.Error("post-eviction 2nd call = true, want false")
	}
}

func TestNewTrackerPanicsOnInvalidMaxEntries(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewTracker(0) did not panic")
		}
	}()
	NewTracker(0)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/spam/... -run TestTracker -v`
Expected: FAIL — `NewTracker`/`Tracker`/`Seen` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `internal/spam/tracker.go`:

```go
package spam

import "sync"

// Tracker records repeat submissions from the same (formID, ip) pair. Bounded by
// maxEntries, evicting the oldest-inserted pair on overflow. Deliberately
// count-based, not time-based (unlike ratelimit.Limiter) — there is no cadence
// requirement here, just a cap on memory. This means it does not persist across
// process restarts; that's an accepted tradeoff (see design doc), not a bug.
type Tracker struct {
	mu         sync.Mutex
	counts     map[string]int
	order      []string
	maxEntries int
}

// NewTracker creates a Tracker bounded to maxEntries distinct (formID, ip) pairs.
func NewTracker(maxEntries int) *Tracker {
	if maxEntries <= 0 {
		panic("spam: maxEntries must be > 0")
	}
	return &Tracker{
		counts:     make(map[string]int),
		maxEntries: maxEntries,
	}
}

// Seen records a submission from (formID, ip) and reports whether this is the
// 3rd or later submission from that pair — the threshold at which repeat
// behavior is treated as spam rather than a legitimate resubmission (e.g. a
// visitor fixing a typo and sending again).
func (t *Tracker) Seen(formID, ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := formID + "\x00" + ip
	if _, exists := t.counts[key]; !exists {
		if len(t.counts) >= t.maxEntries {
			oldest := t.order[0]
			t.order = t.order[1:]
			delete(t.counts, oldest)
		}
		t.order = append(t.order, key)
	}
	t.counts[key]++
	return t.counts[key] >= 3
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/spam/... -race -v`
Expected: PASS — full `internal/spam` package, race-clean.

- [ ] **Step 5: Commit**

```bash
git add internal/spam/tracker.go internal/spam/tracker_test.go
git commit -m "feat: add IP-repeat Tracker to spam package"
```

---

### Task 4: Wire `Tracker` into `SubmitHandler`

**Files:**
- Modify: `internal/handler/submit.go`
- Modify: `internal/handler/submit_test.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `spam.NewTracker(maxEntries int) *spam.Tracker`, `(*spam.Tracker).Seen(formID, ip string) bool` from Task 3.

- [ ] **Step 1: Write the failing tests**

In `internal/handler/submit_test.go`, add `"github.com/youruser/dsforms/internal/spam"` to the import block:

```go
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/mail"
	"github.com/youruser/dsforms/internal/spam"
	"github.com/youruser/dsforms/internal/store"
)
```

Update every `SubmitHandler{...}` literal in the file to include `Tracker: spam.NewTracker(1000)` — there are 5 sites. Each becomes:

```go
h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
```

```go
h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
```

(this second one is `TestSubmitDefaultRedirect`'s local `h`, identical literal to the one above — both need the same edit)

```go
h := &SubmitHandler{Store: s, Notifier: m, Webhook: wh, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
```

(applies to both `TestSubmitWebhookFired` and `TestSubmitNoWebhook`, which share this exact literal)

```go
h := &SubmitHandler{Store: s, Notifier: nil, Webhook: nil, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
```

(`TestSubmitNoEmailNoWebhook`)

Then append two new test functions at the end of the file:

```go
func TestSubmitThirdSameIPDropped(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)

	submit := func() *httptest.ResponseRecorder {
		form := url.Values{"name": {"Alice"}, "message": {"hello there"}}
		req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	submit()
	submit()
	w := submit()

	if w.Code != http.StatusFound {
		t.Errorf("3rd submission status = %d, want 302 (silent drop)", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 2 {
		t.Errorf("submissions = %d, want 2 (1st and 2nd stored, 3rd dropped)", len(subs))
	}
}

func TestSubmitSecondSameIPStillStored(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)

	submit := func() *httptest.ResponseRecorder {
		form := url.Values{"name": {"Alice"}, "message": {"hello there"}}
		req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	submit()
	w := submit()

	if w.Code != http.StatusFound {
		t.Errorf("2nd submission status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 2 {
		t.Errorf("submissions = %d, want 2 (repeat threshold is 3rd, not 2nd)", len(subs))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handler/... -run TestSubmit -v`
Expected: FAIL to compile — `SubmitHandler` has no field `Tracker` yet.

- [ ] **Step 3: Implement — add the field and wire it into `Handle`**

In `internal/handler/submit.go`, add the import and struct field. The import block becomes:

```go
import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/youruser/dsforms/internal/spam"
	"github.com/youruser/dsforms/internal/store"
)
```

(unchanged — `spam` is already imported; no new import needed here)

The `SubmitHandler` struct becomes:

```go
// SubmitHandler handles form submissions via POST /f/{formID}.
type SubmitHandler struct {
	Store    *store.Store
	Notifier Notifier
	Webhook  WebhookSender
	BaseURL  string
	Tracker  *spam.Tracker
}
```

Replace this block in `Handle` (currently right after building `data` and validating it's non-empty):

```go
	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)

	// Content spam — silently drop like the honeypot: look successful, store nothing.
	if spam.IsSpam(data) {
		respondSuccess(w, r, formID, redirectURL)
		return
	}

	ip := ExtractIP(r)
```

with:

```go
	redirectURL := determineRedirect(r.FormValue("_redirect"), form.Redirect)
	ip := ExtractIP(r)

	// Content spam or repeat-IP abuse — silently drop like the honeypot: look
	// successful, store nothing. Tracker.Seen must run unconditionally — it also
	// records the submission, so short-circuiting on IsSpam via || would skip
	// the call and undercount this IP's repeat tally whenever content scoring
	// already caught it first.
	repeated := h.Tracker.Seen(formID, ip)
	if spam.IsSpam(data) || repeated {
		respondSuccess(w, r, formID, redirectURL)
		return
	}
```

Also update the doc comment above `Handle` (currently listing the 11-step flow) — step 7 and the step numbering after it shift:

```go
// Handle processes a form submission.
// Flow:
//  1. Look up form by ID → 404 if missing
//  2. Parse form body
//  3. Honeypot: if _honeypot non-empty → silently succeed without saving
//  4. Filter internal fields, build data map
//  5. Validate: data map must have ≥1 key → else 400
//  6. Determine redirect: _redirect > form.Redirect > /success; extract client IP
//  7. Spam check: if IsSpam(data) or this is the 3rd+ submission from this IP to
//     this form → silently succeed without saving (mirrors honeypot)
//  8. Save submission to DB
//  9. Send email and webhook notifications async
//  10. Respond (JSON or redirect)
```

- [ ] **Step 4: Wire `Tracker` in `main.go`**

Add the import (in the existing alphabetized internal-package import block):

```go
	"github.com/youruser/dsforms/internal/spam"
```

placed between the `"github.com/youruser/dsforms/internal/ratelimit"` and `"github.com/youruser/dsforms/internal/store"` lines to keep the block alphabetized.

Update the `submitHandler` construction:

```go
	submitHandler := &handler.SubmitHandler{
		Store:    s,
		Notifier: mailer,
		Webhook:  webhookSender,
		BaseURL:  cfg.BaseURL,
		Tracker:  spam.NewTracker(10000),
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./... && go test ./... -race`
Expected: PASS, whole module, race-clean — no compile errors, no regressions in any package.

- [ ] **Step 6: Commit**

```bash
git add internal/handler/submit.go internal/handler/submit_test.go main.go
git commit -m "feat: wire IP-repeat Tracker into submit handler"
```

---

## Self-Review Notes

- **Spec coverage:** Pattern 1 (gibberish) → Tasks 1-2. Pattern 2 (IP-repeat bot) → Tasks 3-4. The `Seen`-must-run-unconditionally correctness fix from the spec's self-review is carried into Task 4 Step 3 verbatim. Storage/eviction behavior (bounded LRU, no TTL, no `now` injection) matches the corrected spec exactly. Out-of-scope items (patterns 3/4, persistence, admin UI) are not touched by any task.
- **Type consistency:** `fieldHasGibberish(value string) bool` (Task 1) is called identically in Task 2's `Score`. `NewTracker(maxEntries int) *Tracker` and `(*Tracker).Seen(formID, ip string) bool` (Task 3) match their usage in Task 4's `main.go` and `submit.go` exactly — no signature drift.
- **No placeholders:** every step above has literal code, not descriptions of code.
