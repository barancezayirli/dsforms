// Package safe runs a function without letting a panic escape it.
//
// A panic in a goroutine is not recoverable from anywhere else: recover() only
// catches panics raised in its own goroutine, so an unguarded background worker
// takes the whole process down. Under a container restart policy that reads as a
// crash loop rather than as one failed unit of work, which is the wrong trade
// for a form endpoint — one malformed row should cost one iteration, not the
// HTTP server.
//
// It has no dependencies, so anything from main down can use it.
package safe

import (
	"log"
	"runtime/debug"
)

// Do calls fn, recovering and logging any panic. context names the caller so a
// log line identifies which worker failed.
//
// Use it two ways, both correct:
//
//	go safe.Do("submit: notify", func() { ... })   // one-shot, per goroutine
//	for range ticker.C {                           // per iteration, so the
//		safe.Do("session cleanup", func() { ... }) // loop survives one bad tick
//	}
func Do(context string, fn func()) {
	report(context, fn, log.Printf)
}

// report is Do with the logger passed in. Keeping the seam a parameter rather
// than a package-level variable means tests capture output without mutating
// shared state, so they can still run in parallel.
//
// The stack is logged with the value: a bare panic message rarely says enough to
// find the cause, and this is the only record that the work was attempted.
func report(context string, fn func(), logf func(string, ...any)) {
	defer func() {
		if rec := recover(); rec != nil {
			logf("panic in %s: %v\n%s", context, rec, debug.Stack())
		}
	}()
	fn()
}
