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
