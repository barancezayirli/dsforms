package broadcaster

import (
	"errors"
	"sync"
	"testing"

	"github.com/youruser/dsforms/internal/store"
)

// fakeStore implements the Store interface for deterministic worker tests.
type fakeStore struct {
	mu        sync.Mutex
	pending   []store.Delivery
	sent      []string
	failed    map[string]int // id -> attempts recorded
	broadcast store.Broadcast
	doneCalls []string
}

func newFakeStore(b store.Broadcast, emails []string) *fakeStore {
	f := &fakeStore{broadcast: b, failed: map[string]int{}}
	for i, e := range emails {
		f.pending = append(f.pending, store.Delivery{
			ID: string(rune('a' + i)), BroadcastID: b.ID, Email: e, Status: "pending",
		})
	}
	return f
}

func (f *fakeStore) NextPendingDeliveries(limit int) ([]store.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Delivery
	for _, d := range f.pending {
		if d.Status == "pending" {
			out = append(out, d)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeStore) GetBroadcast(id string) (store.Broadcast, error) {
	return f.broadcast, nil
}

func (f *fakeStore) MarkDeliverySent(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, id)
	for i := range f.pending {
		if f.pending[i].ID == id {
			f.pending[i].Status = "sent"
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
				f.pending[i].Status = "failed"
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
	mu      sync.Mutex
	sent    []string
	failAll bool
}

func (m *fakeMailer) SendMail(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAll {
		return errors.New("smtp down")
	}
	m.sent = append(m.sent, to)
	return nil
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
