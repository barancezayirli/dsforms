package ratelimit

import (
	"sort"
	"sync"
	"time"

	"github.com/barancezayirli/dsforms/internal/safe"
)

type bucket struct {
	tokens   float64
	lastSeen time.Time

	// requests and throttled are counters for the admin overview only; they
	// take no part in the limiting decision. They are in-process and reset on
	// restart — persisting a row per request would put a database write on the
	// hot path this limiter exists to avoid — and StartCleanup drops whole
	// buckets once idle, so the panel is a recent-activity view rather than a
	// since-boot total.
	requests  int
	throttled int
}

// Activity is one IP's traffic, for the admin overview.
type Activity struct {
	IP        string
	Requests  int
	Throttled int
	State     string // "ok" | "throttled"
}

// Limiter implements a per-IP token bucket rate limiter.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   int
	rate    float64
	now     func() time.Time
}

// NewLimiter creates a rate limiter with the given burst size and refill rate.
func NewLimiter(burst, perMinute int, now func() time.Time) *Limiter {
	if burst <= 0 {
		panic("ratelimit: burst must be > 0")
	}
	if perMinute <= 0 {
		panic("ratelimit: perMinute must be > 0")
	}
	if now == nil {
		panic("ratelimit: now function must not be nil")
	}
	return &Limiter{
		buckets: make(map[string]*bucket),
		burst:   burst,
		rate:    float64(perMinute) / 60.0,
		now:     now,
	}
}

// Allow checks if a request from the given IP should be allowed.
func (l *Limiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	t := l.now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: float64(l.burst), lastSeen: t}
		l.buckets[ip] = b
	} else {
		elapsed := t.Sub(b.lastSeen)
		b.tokens += elapsed.Seconds() * l.rate
		if b.tokens > float64(l.burst) {
			b.tokens = float64(l.burst)
		}
		b.lastSeen = t
	}

	b.requests++

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	b.throttled++
	return false
}

// Snapshot returns the busiest IPs currently tracked, busiest first, capped at
// n. Buckets swept by StartCleanup are gone, so this is recent activity rather
// than a total since boot.
//
// Read-only by construction: it iterates the buckets under the same mutex Allow
// uses but never touches tokens or lastSeen. Implementing it in terms of Allow
// would be much shorter and would hand out a token on every page render —
// Allow refills lazily and then decrements, so reading through it would change
// what it reports.
func (l *Limiter) Snapshot(n int) []Activity {
	if n <= 0 {
		return nil
	}

	l.mu.Lock()
	out := make([]Activity, 0, len(l.buckets))
	for ip, b := range l.buckets {
		state := "ok"
		if b.throttled > 0 {
			state = "throttled"
		}
		out = append(out, Activity{IP: ip, Requests: b.requests, Throttled: b.throttled, State: state})
	}
	l.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		// Ties broken by IP so the panel does not reshuffle on every refresh —
		// map iteration order would otherwise make it flicker.
		return out[i].IP < out[j].IP
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (l *Limiter) cleanup(maxAge time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for ip, b := range l.buckets {
		if l.now().Sub(b.lastSeen) > maxAge {
			delete(l.buckets, ip)
		}
	}
}

// StartCleanup runs a background goroutine that removes stale entries.
//
// Guarded per tick: an unrecovered panic in a goroutine takes the whole process
// down, and the rate limiter dying should not take the HTTP server with it.
func (l *Limiter) StartCleanup(interval, maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			safe.Do("ratelimit: limiter cleanup", func() { l.cleanup(maxAge) })
		}
	}()
}

type loginState struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

// LoginGuard tracks failed login attempts and locks out IPs.
type LoginGuard struct {
	mu       sync.Mutex
	attempts map[string]*loginState
	maxFails int
	lockout  time.Duration
	now      func() time.Time
}

// NewLoginGuard creates a login brute-force guard.
func NewLoginGuard(maxFails int, lockout time.Duration, now func() time.Time) *LoginGuard {
	if maxFails <= 0 {
		panic("ratelimit: maxFails must be > 0")
	}
	if now == nil {
		panic("ratelimit: now function must not be nil")
	}
	return &LoginGuard{
		attempts: make(map[string]*loginState),
		maxFails: maxFails,
		lockout:  lockout,
		now:      now,
	}
}

// RecordFailure records a failed login attempt from the given IP.
func (g *LoginGuard) RecordFailure(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	t := g.now()
	s, ok := g.attempts[ip]
	if !ok {
		s = &loginState{}
		g.attempts[ip] = s
	}
	s.failures++
	s.lastSeen = t
	if s.failures >= g.maxFails {
		s.lockedUntil = t.Add(g.lockout)
	}
}

// RecordSuccess records a successful login, resetting the failure counter.
func (g *LoginGuard) RecordSuccess(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.attempts, ip)
}

// IsLocked returns true if the given IP is currently locked out.
func (g *LoginGuard) IsLocked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.attempts[ip]
	if !ok {
		return false
	}
	return g.now().Before(s.lockedUntil)
}

func (g *LoginGuard) cleanup(maxAge time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	t := g.now()
	for ip, s := range g.attempts {
		if t.Sub(s.lastSeen) > maxAge && !t.Before(s.lockedUntil) {
			delete(g.attempts, ip)
		}
	}
}

// StartCleanup runs a background goroutine that removes stale entries. Guarded
// per tick, for the same reason as Limiter.StartCleanup.
func (g *LoginGuard) StartCleanup(interval, maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			safe.Do("ratelimit: login guard cleanup", func() { g.cleanup(maxAge) })
		}
	}()
}
