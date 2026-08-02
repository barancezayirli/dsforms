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
