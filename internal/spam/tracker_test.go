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
