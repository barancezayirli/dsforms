// Package broadcaster runs a background worker that drains pending broadcast
// deliveries, sending one email per recipient with throttling and retry. It is
// safe across restarts: pending deliveries left by a previous run are resumed,
// and a delivery already marked 'sent' is never re-sent.
package broadcaster

import (
	"log"
	"sync"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

// Store is the subset of *store.Store the worker needs.
type Store interface {
	NextPendingDeliveries(limit int) ([]store.Delivery, error)
	GetBroadcast(id string) (store.Broadcast, error)
	MarkDeliverySent(id string) error
	MarkDeliveryFailed(id, errMsg string, maxAttempts int) error
	HasPendingDeliveries(broadcastID string) (bool, error)
	ListSendingBroadcasts() ([]string, error)
	MarkBroadcastDone(id string) error
}

// Compile-time check that the concrete store satisfies the Store interface.
var _ Store = (*store.Store)(nil)

// Mailer sends one email. Implemented by *mail.Mailer.
type Mailer interface {
	SendMail(to, subject, body string) error
}

// Worker drains the delivery queue.
type Worker struct {
	Store       Store
	Mailer      Mailer
	BatchSize   int           // deliveries pulled per pass
	MaxAttempts int           // attempts before a delivery is marked failed
	Throttle    time.Duration // pause between individual sends
	Idle        time.Duration // poll interval when the queue is empty

	signal     chan struct{}
	signalOnce sync.Once
	sleepFn    func() // overridable in tests; nil → time.Sleep(Throttle)
}

func (w *Worker) sleep() {
	if w.sleepFn != nil {
		w.sleepFn()
		return
	}
	if w.Throttle > 0 {
		time.Sleep(w.Throttle)
	}
}

// ensureSignal lazily creates the wakeup channel exactly once, race-free.
func (w *Worker) ensureSignal() {
	w.signalOnce.Do(func() {
		w.signal = make(chan struct{}, 1)
	})
}

// RunOnce processes one batch of pending deliveries, then finalizes any
// broadcasts that have no pending deliveries left. Returns the number processed.
func (w *Worker) RunOnce() (int, error) {
	ds, err := w.Store.NextPendingDeliveries(w.BatchSize)
	if err != nil {
		return 0, err
	}

	bcache := map[string]store.Broadcast{}
	for _, d := range ds {
		b, ok := bcache[d.BroadcastID]
		if !ok {
			loaded, gerr := w.Store.GetBroadcast(d.BroadcastID)
			if gerr != nil {
				log.Printf("broadcaster: get broadcast %s: %v", d.BroadcastID, gerr)
				_ = w.Store.MarkDeliveryFailed(d.ID, "broadcast lookup failed", w.MaxAttempts)
				continue
			}
			bcache[d.BroadcastID] = loaded
			b = loaded
		}

		if w.Mailer == nil {
			_ = w.Store.MarkDeliveryFailed(d.ID, "email sending not configured", w.MaxAttempts)
			continue
		}

		if sendErr := w.Mailer.SendMail(d.Email, b.Subject, b.Body); sendErr != nil {
			if err := w.Store.MarkDeliveryFailed(d.ID, sendErr.Error(), w.MaxAttempts); err != nil {
				log.Printf("broadcaster: mark failed %s: %v", d.ID, err)
			}
		} else {
			if err := w.Store.MarkDeliverySent(d.ID); err != nil {
				log.Printf("broadcaster: mark sent %s: %v", d.ID, err)
			}
		}
		w.sleep()
	}

	w.finalize()
	return len(ds), nil
}

// finalize marks any 'sending' broadcast with no remaining pending deliveries done.
func (w *Worker) finalize() {
	ids, err := w.Store.ListSendingBroadcasts()
	if err != nil {
		log.Printf("broadcaster: list sending broadcasts: %v", err)
		return
	}
	for _, id := range ids {
		pending, err := w.Store.HasPendingDeliveries(id)
		if err != nil {
			log.Printf("broadcaster: has pending %s: %v", id, err)
			continue
		}
		if !pending {
			if err := w.Store.MarkBroadcastDone(id); err != nil {
				log.Printf("broadcaster: mark done %s: %v", id, err)
			}
		}
	}
}

// Start launches the worker goroutine. It runs until the process exits, waking
// on Notify() or every Idle interval to check for new work.
func (w *Worker) Start() {
	if w.BatchSize <= 0 {
		w.BatchSize = 50
	}
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = 3
	}
	if w.Idle <= 0 {
		w.Idle = 5 * time.Second
	}
	w.ensureSignal()
	go func() {
		for {
			n, err := w.RunOnce()
			if err != nil {
				log.Printf("broadcaster: run error: %v", err)
				time.Sleep(w.Idle)
				continue
			}
			if n == 0 {
				select {
				case <-w.signal:
				case <-time.After(w.Idle):
				}
			}
		}
	}()
}

// Notify signals the worker that new deliveries may be available (non-blocking).
func (w *Worker) Notify() {
	w.ensureSignal()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}
