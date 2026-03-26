# Session 3 — Rate Limiter & Security Middleware Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In-process token bucket rate limiter, login brute-force guard, security headers middleware, and request body size limit — all tested with time injection (no time.Sleep).

**Architecture:** Single `internal/ratelimit` package with two structs: `Limiter` (per-IP token bucket for form submissions) and `LoginGuard` (failed attempt tracker with lockout). Both use `sync.Mutex` and accept a `func() time.Time` parameter for deterministic testing. Cleanup goroutines run periodically to prevent memory growth. Security headers and MaxBytesReader are middlewares wired in `main.go`.

**Tech Stack:** Go stdlib only (`sync`, `time`, `net/http`). No new dependencies.

---

## Deferred item from Session 1 to address

SESSION_PROGRESS.md notes: "Integer config values (SMTP_PORT, RATE_BURST, RATE_PER_MINUTE) have no range validation — address in Session 3". We'll validate these in the Limiter/LoginGuard constructors — they panic on invalid values (burst <= 0, perMinute <= 0, maxFails <= 0). Tests included.

## Deviation from session spec

DSFORMS_SESSIONS.md says MaxBytesReader should return "400 response" but `http.MaxBytesReader` triggers a 413 (Request Entity Too Large) which is semantically correct. Our tests check for 413.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/ratelimit/ratelimit.go` | Limiter struct (token bucket), LoginGuard struct (brute-force), cleanup goroutines |
| `internal/ratelimit/ratelimit_test.go` | Table-driven tests with time injection for both structs |
| `main.go` | Wire security headers middleware, MaxBytesReader middleware (modify existing) |
| `main_test.go` | Tests for security headers and MaxBytesReader middleware (modify existing) |

---

## API Design

```go
// Limiter — per-IP token bucket rate limiter
type Limiter struct {
    mu      sync.Mutex
    buckets map[string]*bucket
    burst   int
    rate    float64  // tokens per second
    now     func() time.Time
}

func NewLimiter(burst, perMinute int, now func() time.Time) *Limiter
func (l *Limiter) Allow(ip string) bool
func (l *Limiter) StartCleanup(interval, maxAge time.Duration) // goroutine

// LoginGuard — brute-force login protection
type LoginGuard struct {
    mu       sync.Mutex
    attempts map[string]*loginState
    maxFails int
    lockout  time.Duration
    now      func() time.Time
}

func NewLoginGuard(maxFails int, lockout time.Duration, now func() time.Time) *LoginGuard
func (g *LoginGuard) RecordFailure(ip string)
func (g *LoginGuard) RecordSuccess(ip string)
func (g *LoginGuard) IsLocked(ip string) bool
func (g *LoginGuard) StartCleanup(interval, maxAge time.Duration) // goroutine
```

---

### Task 1: Write Limiter tests (red phase)

**Files:**
- Create: `internal/ratelimit/ratelimit_test.go`
- Create: `internal/ratelimit/ratelimit.go` (minimal stub)

- [ ] **Step 1: Write Limiter tests**

```go
package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterFirstBurstSucceeds(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := NewLimiter(5, 6, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d rejected, expected allowed", i+1)
		}
	}
}

func TestLimiterBurstExhausted(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := NewLimiter(5, 6, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		l.Allow("1.2.3.4")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("6th request allowed, expected rejected")
	}
}

func TestLimiterTokensRefill(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := func() time.Time { return now }
	l := NewLimiter(5, 6, clock)
	// Exhaust all tokens
	for i := 0; i < 5; i++ {
		l.Allow("1.2.3.4")
	}
	// Advance time by 10 seconds → should refill 1 token (6 per minute = 0.1 per second)
	now = now.Add(10 * time.Second)
	if !l.Allow("1.2.3.4") {
		t.Fatal("request rejected after 10s refill, expected allowed")
	}
}

func TestLimiterIndependentIPs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := NewLimiter(2, 6, func() time.Time { return now })
	l.Allow("1.1.1.1")
	l.Allow("1.1.1.1")
	// IP 1 exhausted
	if l.Allow("1.1.1.1") {
		t.Fatal("IP 1 should be exhausted")
	}
	// IP 2 should still work
	if !l.Allow("2.2.2.2") {
		t.Fatal("IP 2 should be allowed")
	}
}

func TestLimiterCleanup(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := NewLimiter(5, 6, func() time.Time { return now })
	l.Allow("1.2.3.4")
	// Advance past maxAge
	now = now.Add(31 * time.Minute)
	l.cleanup(30 * time.Minute)
	// Bucket should be gone — next Allow creates fresh bucket with full tokens
	l.Allow("1.2.3.4") // uses 1 of 5
	l.Allow("1.2.3.4") // uses 2 of 5
	l.Allow("1.2.3.4") // uses 3 of 5
	l.Allow("1.2.3.4") // uses 4 of 5
	l.Allow("1.2.3.4") // uses 5 of 5
	if l.Allow("1.2.3.4") {
		t.Fatal("expected rejection after cleanup and re-exhaust")
	}
}
```

```go
func TestNewLimiterPanicsOnInvalidBurst(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for burst=0")
		}
	}()
	NewLimiter(0, 6, time.Now)
}

func TestNewLimiterPanicsOnInvalidRate(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for perMinute=0")
		}
	}()
	NewLimiter(5, 0, time.Now)
}
```

Note: `cleanup` is an unexported method called directly in tests (same package). `StartCleanup` wraps it in a goroutine with a ticker.

- [ ] **Step 2: Write minimal stub ratelimit.go**

```go
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   int
	rate    float64
	now     func() time.Time
}

func NewLimiter(burst, perMinute int, now func() time.Time) *Limiter {
	return nil
}

func (l *Limiter) Allow(ip string) bool {
	return false
}

func (l *Limiter) cleanup(maxAge time.Duration) {}

func (l *Limiter) StartCleanup(interval, maxAge time.Duration) {}

type loginState struct {
	failures    int
	lockedUntil time.Time
}

type LoginGuard struct {
	mu       sync.Mutex
	attempts map[string]*loginState
	maxFails int
	lockout  time.Duration
	now      func() time.Time
}

func NewLoginGuard(maxFails int, lockout time.Duration, now func() time.Time) *LoginGuard {
	return nil
}

func (g *LoginGuard) RecordFailure(ip string) {}

func (g *LoginGuard) RecordSuccess(ip string) {}

func (g *LoginGuard) IsLocked(ip string) bool {
	return false
}

func (g *LoginGuard) cleanup(maxAge time.Duration) {}

func (g *LoginGuard) StartCleanup(interval, maxAge time.Duration) {}
```

- [ ] **Step 3: Run tests — confirm FAIL**

```bash
go test ./internal/ratelimit/... -v -count=1
```

- [ ] **Step 4: Commit**

```bash
git add internal/ratelimit/
git commit -m "test: add token bucket limiter tests (red)"
```

---

### Task 2: Implement Limiter (green phase)

**Files:**
- Modify: `internal/ratelimit/ratelimit.go`

- [ ] **Step 1: Implement NewLimiter, Allow, cleanup, StartCleanup**

Key algorithm for `Allow(ip)`:
1. Lock mutex
2. Get or create bucket for IP (new bucket starts with `burst` tokens)
3. Compute elapsed since lastSeen
4. Refill: `tokens += elapsed.Seconds() * rate` (capped at `burst`)
5. Update lastSeen to now
6. If tokens >= 1: consume 1, return true
7. Else: return false

`cleanup(maxAge)`: iterate buckets, remove any where `now() - lastSeen > maxAge`

`StartCleanup`: launch goroutine with `time.NewTicker(interval)`, call cleanup on each tick

- [ ] **Step 2: Run tests — confirm PASS**

```bash
go test ./internal/ratelimit/... -v -count=1
go test -race ./internal/ratelimit/... -count=1
```

- [ ] **Step 3: Commit**

```bash
git add internal/ratelimit/
git commit -m "feat: implement token bucket rate limiter with time injection"
```

---

### Task 3: Write LoginGuard tests (red phase) + implement (green phase)

**Files:**
- Modify: `internal/ratelimit/ratelimit_test.go`
- Modify: `internal/ratelimit/ratelimit.go`

- [ ] **Step 1: Write LoginGuard tests**

```go
func TestLoginGuardFirstFailsDoNotLock(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g := NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })
	for i := 0; i < 4; i++ {
		g.RecordFailure("1.2.3.4")
	}
	if g.IsLocked("1.2.3.4") {
		t.Fatal("locked after 4 failures, expected unlocked")
	}
}

func TestLoginGuardFifthFailureLocks(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g := NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		g.RecordFailure("1.2.3.4")
	}
	if !g.IsLocked("1.2.3.4") {
		t.Fatal("not locked after 5 failures, expected locked")
	}
}

func TestNewLoginGuardPanicsOnInvalidMaxFails(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for maxFails=0")
		}
	}()
	NewLoginGuard(0, 15*time.Minute, time.Now)
}

func TestLoginGuardLockoutExpires(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g := NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		g.RecordFailure("1.2.3.4")
	}
	// Advance past lockout
	now = now.Add(16 * time.Minute)
	if g.IsLocked("1.2.3.4") {
		t.Fatal("still locked after lockout expired")
	}
}

func TestLoginGuardSuccessResets(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g := NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })
	for i := 0; i < 4; i++ {
		g.RecordFailure("1.2.3.4")
	}
	g.RecordSuccess("1.2.3.4")
	// After reset, 4 more failures should not lock
	for i := 0; i < 4; i++ {
		g.RecordFailure("1.2.3.4")
	}
	if g.IsLocked("1.2.3.4") {
		t.Fatal("locked after reset + 4 failures, expected unlocked")
	}
}

func TestLoginGuardCleanup(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g := NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })
	g.RecordFailure("1.2.3.4")
	now = now.Add(31 * time.Minute)
	g.cleanup(30 * time.Minute)
	// Entry should be gone — failure count reset
	for i := 0; i < 4; i++ {
		g.RecordFailure("1.2.3.4")
	}
	if g.IsLocked("1.2.3.4") {
		t.Fatal("locked after cleanup + 4 failures, expected unlocked")
	}
}
```

- [ ] **Step 2: Run tests — confirm new tests FAIL**

- [ ] **Step 3: Implement LoginGuard methods**

`RecordFailure(ip)`: increment failures, if failures >= maxFails set lockedUntil = now() + lockout
`RecordSuccess(ip)`: delete the entry for IP
`IsLocked(ip)`: return true if entry exists AND now() < lockedUntil
`cleanup(maxAge)`: remove entries where now() - lastActivity > maxAge (use lockedUntil as activity marker, or add a lastSeen field)

- [ ] **Step 4: Run ALL ratelimit tests — confirm PASS**

```bash
go test ./internal/ratelimit/... -v -count=1
go test -race ./internal/ratelimit/... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/ratelimit/
git commit -m "feat: implement login brute-force guard with lockout and cleanup"
```

---

### Task 4: Wire security headers + MaxBytesReader middleware in main.go

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`

- [ ] **Step 1: Write middleware tests in main_test.go**

```go
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	r := newRouter()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.header, func(t *testing.T) {
			t.Parallel()
			got := w.Header().Get(tt.header)
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestMaxBytesReader(t *testing.T) {
	t.Parallel()
	r := newRouter()
	// Add a test route that reads the body
	r.Post("/test-body", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Body over 64KB should fail
	bigBody := make([]byte, 65*1024)
	req := httptest.NewRequest("POST", "/test-body", bytes.NewReader(bigBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}
```

Add `"bytes"` and `"io"` to main_test.go imports.

- [ ] **Step 2: Run tests — confirm FAIL** (middleware not wired yet)

- [ ] **Step 3: Wire middlewares in newRouter()**

In `main.go`, update `newRouter()` to add security headers and MaxBytesReader middleware:

```go
func newRouter() *chi.Mux {
	r := chi.NewRouter()

	// Security headers on all responses
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
			next.ServeHTTP(w, r)
		})
	})

	// Request body size limit (64KB)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
			next.ServeHTTP(w, r)
		})
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Printf("healthz write error: %v", err)
		}
	})

	return r
}
```

- [ ] **Step 4: Run ALL tests**

```bash
go test ./... -race -count=1
go vet ./...
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add security headers and MaxBytesReader middleware"
```

---

### Task 5: Final verification

- [ ] **Step 1: Full checks**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
```

All must exit 0. No `time.Sleep` in any test file.

- [ ] **Step 2: Verify no time.Sleep in tests**

```bash
grep -r "time.Sleep" internal/ratelimit/ main_test.go
```

Expected: no matches.
