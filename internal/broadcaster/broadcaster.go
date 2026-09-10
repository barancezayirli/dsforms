// Package broadcaster runs a background worker that drains pending broadcast
// deliveries, sending one email per recipient with throttling and retry. It is
// safe across restarts: pending deliveries left by a previous run are resumed.
// Idempotency is enforced by the store — NextPendingDeliveries returns only
// status='pending' rows, so deliveries already marked sent are never fetched again.
package broadcaster

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/barancezayirli/dsforms/internal/safe"
	"github.com/barancezayirli/dsforms/internal/store"
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

// Worker drains the delivery queue. Exported configuration fields (Store,
// Mailer, BatchSize, MaxAttempts, Throttle, Idle) must be set before Start()
// and must not be mutated afterward (they are read by the worker goroutine
// without synchronization).
type Worker struct {
	Store       Store
	Mailer      Mailer
	BatchSize   int           // deliveries pulled per pass
	MaxAttempts int           // attempts before a delivery is marked failed
	Throttle    time.Duration // pause between individual sends
	Idle        time.Duration // poll interval when the queue is empty

	signal     chan struct{}
	signalOnce sync.Once
	startOnce  sync.Once
	sleepFn    func() // overridable in tests; nil → time.Sleep(Throttle) when Throttle > 0, else no-op
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
				if err := w.Store.MarkDeliveryFailed(d.ID, "broadcast lookup failed", w.MaxAttempts); err != nil {
					log.Printf("broadcaster: mark failed (lookup) %s: %v", d.ID, err)
				}
				continue
			}
			bcache[d.BroadcastID] = loaded
			b = loaded
		}

		if w.Mailer == nil {
			if err := w.Store.MarkDeliveryFailed(d.ID, "email sending not configured", w.MaxAttempts); err != nil {
				log.Printf("broadcaster: mark failed (no mailer) %s: %v", d.ID, err)
			}
			continue
		}

		// The send is guarded per delivery, not per batch. A panic here used to
		// escape RunOnce, and once the worker loop recovered it the loop saw
		// n = 0, err = nil — indistinguishable from an empty queue. So the
		// poisoned row was retried every Idle interval forever, its attempts
		// never incremented, its broadcast never finalized, and the operator's
		// progress page never resolved. Treating a panic as a failed send lets
		// it exhaust MaxAttempts and stop, which is what an erroring send
		// already did.
		sendErr := w.sendGuarded(d.Email, b.Subject, b.Body)
		if sendErr != nil {
			if err := w.Store.MarkDeliveryFailed(d.ID, sendErr.Error(), w.MaxAttempts); err != nil {
				log.Printf("broadcaster: mark failed %s: %v", d.ID, err)
			}
		} else if err := w.Store.MarkDeliverySent(d.ID); err != nil {
			// The mail went out and we cannot record it. Logging and moving on
			// leaves the row pending with its attempts untouched, so the next
			// cycle sends the identical broadcast to the same person, forever —
			// the same poisoned-row shape the comment above describes, on the
			// write instead of the send.
			//
			// So it is counted as an attempt. That is deliberately the safer lie:
			// the row ends up labelled failed when the mail actually arrived,
			// while the alternative is unbounded duplicates to a real recipient.
			// The error text carries the truth for whoever reads the row, and
			// MaxAttempts caps the duplicates at a handful instead of forever.
			log.Printf("broadcaster: delivery %s was SENT but could not be recorded: %v", d.ID, err)
			recordErr := fmt.Sprintf("delivered, but the result could not be saved "+
				"(%v) — this address may receive the broadcast more than once", err)
			if err := w.Store.MarkDeliveryFailed(d.ID, recordErr, w.MaxAttempts); err != nil {
				log.Printf("broadcaster: delivery %s cannot be recorded at all "+
					"(sent=%v, failed=%v); it will be retried until the database "+
					"accepts a write", d.ID, err, recordErr)
			}
		}
		w.sleep()
	}

	w.finalize()
	return len(ds), nil
}

// runOnceSafely is RunOnce with any escaping panic turned into an error.
//
// The distinction matters more than it looks. Recovering and leaving n = 0,
// err = nil is indistinguishable from "the queue is empty", so the loop takes
// the idle wait and retries the same poisoned batch forever without ever
// advancing its attempt count. Surfacing it as an error routes it to the
// sleep-and-log path, where a persistent failure at least stays visible.
func (w *Worker) runOnceSafely() (n int, err error) {
	panicked := true
	safe.Do("broadcaster: run", func() {
		n, err = w.RunOnce()
		panicked = false
	})
	if panicked {
		return 0, errors.New("panic in RunOnce")
	}
	return n, err
}

// sendGuarded calls the mailer, converting a panic into an ordinary error so one
// malformed recipient costs that delivery rather than the batch or the process.
func (w *Worker) sendGuarded(to, subject, body string) (err error) {
	panicked := true
	safe.Do("broadcaster: send to "+to, func() {
		err = w.Mailer.SendMail(to, subject, body)
		panicked = false
	})
	if panicked {
		return fmt.Errorf("panic while sending to %s", to)
	}
	return err
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
// on Notify() or every Idle interval to check for new work. Calling Start()
// more than once is a no-op; only one goroutine is ever launched.
func (w *Worker) Start() {
	w.startOnce.Do(func() {
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
				n, err := w.runOnceSafely()
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
	})
}

// Notify signals the worker that new deliveries may be available (non-blocking).
func (w *Worker) Notify() {
	w.ensureSignal()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}
