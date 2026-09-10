package broadcaster

import (
	"errors"
	"sync"
	"testing"

	"github.com/barancezayirli/dsforms/internal/store"
)

// fakeStore implements the Store interface for deterministic worker tests.
type fakeStore struct {
	mu               sync.Mutex
	pending          []store.Delivery
	sent             []string
	failed           map[string]int // id -> attempts recorded
	broadcast        store.Broadcast
	doneCalls        []string
	failNextPending  bool
	failGetBroadcast bool
	failMarkSent     bool
}

func newFakeStore(b store.Broadcast, emails []string) *fakeStore {
	f := &fakeStore{broadcast: b, failed: map[string]int{}}
	for i, e := range emails {
		f.pending = append(f.pending, store.Delivery{
			ID: string(rune('a' + i)), BroadcastID: b.ID, Email: e, Status: store.DeliveryStatusPending,
		})
	}
	return f
}

func (f *fakeStore) NextPendingDeliveries(limit int) ([]store.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextPending {
		return nil, errors.New("next pending boom")
	}
	var out []store.Delivery
	for _, d := range f.pending {
		if d.Status == store.DeliveryStatusPending {
			out = append(out, d)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// failedCount reports how many times MarkDeliveryFailed ran for a delivery.
func (f *fakeStore) failedCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failed[id]
}

// statusOf reports a delivery's current status.
func (f *fakeStore) statusOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.pending {
		if d.ID == id {
			return d.Status
		}
	}
	return ""
}

func (f *fakeStore) GetBroadcast(id string) (store.Broadcast, error) {
	if f.failGetBroadcast {
		return store.Broadcast{}, errors.New("get broadcast boom")
	}
	return f.broadcast, nil
}

func (f *fakeStore) MarkDeliverySent(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMarkSent {
		return errors.New("mark sent boom")
	}
	f.sent = append(f.sent, id)
	for i := range f.pending {
		if f.pending[i].ID == id {
			f.pending[i].Status = store.DeliveryStatusSent
		}
	}
	return nil
}

func (f *fakeStore) MarkDeliveryFailed(id, errMsg string, maxAttempts int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[id]++
	for i := range f.pending {
		if f.pending[i].ID == id {
			f.pending[i].Attempts++
			if f.pending[i].Attempts >= maxAttempts {
				f.pending[i].Status = store.DeliveryStatusFailed
			}
		}
	}
	return nil
}

func (f *fakeStore) HasPendingDeliveries(broadcastID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.pending {
		if d.Status == "pending" {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ListSendingBroadcasts() ([]string, error) {
	return []string{f.broadcast.ID}, nil
}

func (f *fakeStore) MarkBroadcastDone(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doneCalls = append(f.doneCalls, id)
	return nil
}

// fakeMailer records sends and can fail on demand.
type fakeMailer struct {
	mu       sync.Mutex
	sent     []string
	failAll  bool
	panicAll bool
	calls    int
}

func (m *fakeMailer) SendMail(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.panicAll {
		panic("mailer exploded")
	}
	if m.failAll {
		return errors.New("smtp down")
	}
	m.sent = append(m.sent, to)
	return nil
}

func (m *fakeMailer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newTestWorker(fs *fakeStore, fm *fakeMailer) *Worker {
	return &Worker{
		Store:       fs,
		Mailer:      fm,
		BatchSize:   10,
		MaxAttempts: 3,
		sleepFn:     func() {}, // no real sleep in tests
	}
}

func TestRunOnceSendsAndFinalizes(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1", Subject: "Hi", Body: "Body"}, []string{"a@x.com", "b@x.com"})
	fm := &fakeMailer{}
	w := newTestWorker(fs, fm)

	n, err := w.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce error = %v", err)
	}
	if n != 2 {
		t.Errorf("processed = %d, want 2", n)
	}
	if len(fm.sent) != 2 {
		t.Errorf("emails sent = %d, want 2", len(fm.sent))
	}
	if len(fs.doneCalls) != 1 {
		t.Errorf("MarkBroadcastDone calls = %d, want 1 (no pending left)", len(fs.doneCalls))
	}
}

func TestRunOnceRetriesUntilCap(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1"}, []string{"a@x.com"})
	fm := &fakeMailer{failAll: true}
	w := newTestWorker(fs, fm)

	for i := 0; i < 3; i++ {
		if _, err := w.RunOnce(); err != nil {
			t.Fatalf("RunOnce error = %v", err)
		}
	}
	if fs.failed["a"] != 3 {
		t.Errorf("failed attempts = %d, want 3", fs.failed["a"])
	}
	hasPending, _ := fs.HasPendingDeliveries("b1")
	if hasPending {
		t.Error("still pending after cap, want failed")
	}
	if len(fs.doneCalls) == 0 {
		t.Error("expected broadcast finalized after cap reached")
	}
}

func TestRunOnceNilMailerFails(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1"}, []string{"a@x.com"})
	w := newTestWorker(fs, nil)
	w.Mailer = nil

	if _, err := w.RunOnce(); err != nil {
		t.Fatalf("RunOnce error = %v", err)
	}
	if fs.failed["a"] == 0 {
		t.Error("expected delivery marked failed when mailer is nil")
	}
}

func TestRunOnceReturnsErrorWhenNextPendingFails(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1"}, []string{"a@x.com"})
	fs.failNextPending = true
	w := newTestWorker(fs, &fakeMailer{})
	n, err := w.RunOnce()
	if err == nil {
		t.Error("RunOnce should surface the NextPendingDeliveries error")
	}
	if n != 0 {
		t.Errorf("processed = %d, want 0", n)
	}
}

func TestRunOnceMarksFailedWhenGetBroadcastFails(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1", Subject: "S", Body: "B"}, []string{"a@x.com"})
	fs.failGetBroadcast = true
	fm := &fakeMailer{}
	w := newTestWorker(fs, fm)
	if _, err := w.RunOnce(); err != nil {
		t.Fatalf("RunOnce error = %v", err)
	}
	if len(fm.sent) != 0 {
		t.Errorf("no email should be sent when broadcast lookup fails; sent = %d", len(fm.sent))
	}
	if fs.failed["a"] == 0 {
		t.Error("delivery should be marked failed when GetBroadcast fails")
	}
}

// TestRunOncePanicExhaustsAttempts pins that a panicking send is treated like a
// failing send rather than like an empty queue.
//
// safe.Do around the worker loop stopped a panic killing the process, but left
// n and err at their zero values — so the loop read "no error, nothing to do",
// took the idle wait, and tried the same poisoned delivery again on the next
// tick. Forever, at Idle cadence, with MarkDeliveryFailed never called: attempts
// never incremented, the row stayed pending, the broadcast never finalized, and
// the operator's progress page showed a spinner that would never resolve.
//
// Guarding the send itself means the poisoned row exhausts MaxAttempts and stops,
// which is what a send that always errors already did.
func TestRunOncePanicExhaustsAttempts(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1", Subject: "Hi", Body: "Body"}, []string{"a@x.com"})
	fm := &fakeMailer{panicAll: true}
	w := newTestWorker(fs, fm)

	for i := 0; i < w.MaxAttempts; i++ {
		if _, err := w.RunOnce(); err != nil {
			t.Fatalf("run %d: RunOnce returned %v; a panicking send must not abort the batch", i, err)
		}
	}

	if got := fs.failedCount("a"); got == 0 {
		t.Fatal("MarkDeliveryFailed was never called — the attempt counter never moved, so this row retries forever")
	}
	if st := fs.statusOf("a"); st != store.DeliveryStatusFailed {
		t.Errorf("delivery status = %q after %d attempts, want %q", st, w.MaxAttempts, store.DeliveryStatusFailed)
	}
}

// And a panic must not stop the rest of the batch: one poisoned recipient should
// not cost the other nine.
func TestRunOncePanicDoesNotAbortTheBatch(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1", Subject: "Hi", Body: "Body"},
		[]string{"a@x.com", "b@x.com", "c@x.com"})
	fm := &poisonMailer{poison: "b@x.com"}
	w := newTestWorker(fs, nil)
	w.Mailer = fm

	if _, err := w.RunOnce(); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(fm.delivered()) != 2 {
		t.Errorf("delivered %v, want the two healthy recipients", fm.delivered())
	}
}

// poisonMailer panics for one address and succeeds for the rest.
type poisonMailer struct {
	mu     sync.Mutex
	poison string
	ok     []string
}

func (m *poisonMailer) SendMail(to, subject, body string) error {
	if to == m.poison {
		panic("poisoned recipient")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ok = append(m.ok, to)
	return nil
}

func (m *poisonMailer) delivered() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ok...)
}

// TestRunOnceStopsResendingWhenTheSendCannotBeRecorded is the delivery-side twin
// of TestRunOncePanicExhaustsAttempts, and it covers the branch that was left
// out when that one was written.
//
// The mail goes out, and then MarkDeliverySent fails. The row keeps status
// 'pending' and its attempts are untouched, so the next cycle picks up the same
// delivery and sends the identical broadcast again — and again, with nothing
// bounding it. The recipient is mailed forever, the progress page permanently
// understates the count, and finalize never completes because
// HasPendingDeliveries never goes false.
//
// The comment above the send in RunOnce reasons about exactly this poisoned-row
// shape for a panicking mailer, and stops one branch short of the write that
// records the success.
func TestRunOnceStopsResendingWhenTheSendCannotBeRecorded(t *testing.T) {
	t.Parallel()
	fs := newFakeStore(store.Broadcast{ID: "b1", Subject: "Hi", Body: "Body"}, []string{"a@x.com"})
	fs.failMarkSent = true
	fm := &fakeMailer{}
	w := newTestWorker(fs, fm)

	// Run well past the cap. If nothing advances the attempt counter this loops
	// as long as the process does.
	runs := w.MaxAttempts + 3
	for i := 0; i < runs; i++ {
		if _, err := w.RunOnce(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	if fm.callCount() > w.MaxAttempts {
		t.Errorf("the same broadcast was mailed %d times over %d cycles.\n"+
			"A send that cannot be recorded must still count as an attempt, or the "+
			"recipient receives it every cycle until someone notices.",
			fm.callCount(), runs)
	}
	if st := fs.statusOf("a"); st != store.DeliveryStatusFailed {
		t.Errorf("delivery status = %q after %d cycles, want %q.\n"+
			"The row is still pending, so it will be picked up again forever.",
			st, runs, store.DeliveryStatusFailed)
	}
	if got := fs.failedCount("a"); got == 0 {
		t.Error("MarkDeliveryFailed was never called, so the attempt counter never " +
			"moved and nothing bounds the retries")
	}
}
