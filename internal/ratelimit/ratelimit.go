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
	return nil
}

// Allow checks if a request from the given IP should be allowed.
func (l *Limiter) Allow(ip string) bool {
	return false
}

func (l *Limiter) cleanup(maxAge time.Duration) {}

// StartCleanup runs a background goroutine that removes stale entries.
func (l *Limiter) StartCleanup(interval, maxAge time.Duration) {}

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
