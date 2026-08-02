package spam

import (
	"sync"
	"testing"
)

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

func TestTrackerConcurrentAccess(t *testing.T) {
	// Do NOT use t.Parallel() — this test deliberately exercises concurrent
	// access to a shared Tracker instance to verify mutex correctness under
	// -race. Each parallel subtest uses t.Parallel() but this parent must run
	// serially to actually stress the shared Tracker's locking.
	tr := NewTracker(10)
	var wg sync.WaitGroup
	numGoroutines := 20

	// Launch goroutines that hit the shared Tracker with mixed (formID, ip)
	// pairs to exercise concurrent counter increments (same pair), concurrent
	// map inserts (different pairs), and potential eviction races.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Each goroutine makes multiple calls.
			// - Some hit the same pair repeatedly (counter stress)
			// - Some hit different pairs (map/order stress)
			// - Mix ensures eviction races if capacity is exceeded
			for j := 0; j < 5; j++ {
				// All goroutines with id < 5 hit the same key, stressing
				// concurrent increments of counts["form-a\x001.1.1.1"].
				if id < 5 {
					tr.Seen("form-a", "1.1.1.1")
				} else {
					// Remaining goroutines each hit unique pairs, stressing
					// concurrent inserts and potential evictions.
					formID := "form-b"
					ip := "2.2.2." + string(rune('0'+byte(id)))
					tr.Seen(formID, ip)
				}
			}
		}(i)
	}
	wg.Wait()
	// If we reach here without a race detector panic or segfault, the mutex
	// is protecting all concurrent accesses correctly.
}
