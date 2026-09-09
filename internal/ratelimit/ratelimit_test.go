package ratelimit

import (
	"fmt"
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
	l := NewLimiter(5, 6, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		l.Allow("1.2.3.4")
	}
	// Advance 10 seconds: 6/min = 0.1/sec, 10s = 1 token refilled
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
	if l.Allow("1.1.1.1") {
		t.Fatal("IP 1 should be exhausted")
	}
	if !l.Allow("2.2.2.2") {
		t.Fatal("IP 2 should be allowed")
	}
}

func TestLimiterCleanup(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := NewLimiter(5, 6, func() time.Time { return now })
	l.Allow("1.2.3.4")
	now = now.Add(31 * time.Minute)
	l.cleanup(30 * time.Minute)
	// After cleanup, bucket is gone. Fresh bucket has full tokens.
	for i := 0; i < 5; i++ {
		l.Allow("1.2.3.4")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("expected rejection after cleanup and re-exhaust")
	}
}

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
	for i := 0; i < 4; i++ {
		g.RecordFailure("1.2.3.4")
	}
	if g.IsLocked("1.2.3.4") {
		t.Fatal("locked after cleanup + 4 failures, expected unlocked")
	}
}

func TestLoginGuardIndependentIPs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g := NewLoginGuard(5, 15*time.Minute, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		g.RecordFailure("1.1.1.1")
	}
	if g.IsLocked("2.2.2.2") {
		t.Fatal("different IP should not be locked")
	}
}

func TestNewLimiterPanicsOnNilNow(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil now")
		}
	}()
	NewLimiter(5, 6, nil)
}

func TestNewLoginGuardPanicsOnNilNow(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil now")
		}
	}()
	NewLoginGuard(5, 15*time.Minute, nil)
}

// TestSnapshot covers the read-only view the admin overview renders. It is
// deliberately not persisted: writing a row per request would put a SQLite
// write on the hot path this limiter exists to avoid, so the panel shows
// activity since the last restart and the UI says so.
func TestSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 6, func() time.Time { return now })

	// Two IPs under the limit, one over it.
	l.Allow("198.51.100.1")
	for i := 0; i < 2; i++ {
		l.Allow("198.51.100.2")
	}
	blocked := false
	for i := 0; i < 6; i++ {
		if !l.Allow("203.0.113.9") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("fixture is wrong: the third IP should have been throttled")
	}

	snap := l.Snapshot(10)
	if len(snap) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(snap), snap)
	}

	// Busiest first — the panel is for spotting an offender.
	if snap[0].IP != "203.0.113.9" {
		t.Errorf("first entry = %s, want the busiest IP", snap[0].IP)
	}
	if snap[0].Requests != 6 {
		t.Errorf("requests = %d, want 6", snap[0].Requests)
	}
	if snap[0].Throttled != 3 {
		t.Errorf("throttled = %d, want 3 (6 requests against a burst of 3)", snap[0].Throttled)
	}
	if snap[0].State != "throttled" {
		t.Errorf("state = %q, want %q", snap[0].State, "throttled")
	}
	for _, e := range snap[1:] {
		if e.State != "ok" {
			t.Errorf("%s state = %q, want ok", e.IP, e.State)
		}
		if e.Throttled != 0 {
			t.Errorf("%s throttled = %d, want 0", e.IP, e.Throttled)
		}
	}
}

func TestSnapshotLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := NewLimiter(5, 6, func() time.Time { return now })
	for i := 0; i < 10; i++ {
		l.Allow(fmt.Sprintf("198.51.100.%d", i))
	}
	if got := len(l.Snapshot(4)); got != 4 {
		t.Errorf("Snapshot(4) returned %d entries, want 4", got)
	}
	if got := len(l.Snapshot(0)); got != 0 {
		t.Errorf("Snapshot(0) returned %d entries, want 0", got)
	}
}

// Snapshot must not disturb the limiter it reads. Allow refills lazily on every
// call, so a Snapshot implemented in terms of Allow would hand out tokens.
func TestSnapshotDoesNotConsumeTokens(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(2, 6, func() time.Time { return now })

	l.Allow("198.51.100.1")
	for i := 0; i < 5; i++ {
		l.Snapshot(10)
	}
	if !l.Allow("198.51.100.1") {
		t.Error("Snapshot consumed a token: the second request should still be allowed")
	}
	if l.Allow("198.51.100.1") {
		t.Error("fixture is wrong: the third request should exceed the burst of 2")
	}
}

func TestSnapshotEmpty(t *testing.T) {
	t.Parallel()
	l := NewLimiter(5, 6, time.Now)
	if got := l.Snapshot(10); len(got) != 0 {
		t.Errorf("Snapshot on an unused limiter = %+v, want empty", got)
	}
}
