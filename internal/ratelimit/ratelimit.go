package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens   float64
	lastSeen time.Time
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

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{
			tokens:   float64(l.burst),
			lastSeen: l.now(),
		}
		l.buckets[ip] = b
	} else {
		elapsed := l.now().Sub(b.lastSeen)
		b.tokens += elapsed.Seconds() * l.rate
		if b.tokens > float64(l.burst) {
			b.tokens = float64(l.burst)
		}
		b.lastSeen = l.now()
	}

	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
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
func (l *Limiter) StartCleanup(interval, maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			l.cleanup(maxAge)
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
	return nil
}

// RecordFailure records a failed login attempt from the given IP.
func (g *LoginGuard) RecordFailure(ip string) {}

// RecordSuccess records a successful login, resetting the failure counter.
func (g *LoginGuard) RecordSuccess(ip string) {}

// IsLocked returns true if the given IP is currently locked out.
func (g *LoginGuard) IsLocked(ip string) bool {
	return false
}

func (g *LoginGuard) cleanup(maxAge time.Duration) {}

// StartCleanup runs a background goroutine that removes stale entries.
func (g *LoginGuard) StartCleanup(interval, maxAge time.Duration) {}
