package safe

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// discard is a logger that throws output away, for the cases asserting
// behaviour rather than reporting.
func discard(string, ...any) {}

func TestDoRunsTheFunction(t *testing.T) {
	t.Parallel()
	ran := false
	Do("ctx", func() { ran = true })
	if !ran {
		t.Error("Do did not call fn")
	}
}

func TestDoContainsAPanic(t *testing.T) {
	t.Parallel()
	// The whole point: this must not take the test binary down with it.
	report("ctx", func() { panic("boom") }, discard)
}

func TestDoReportsThePanic(t *testing.T) {
	t.Parallel()
	var got string
	capture := func(format string, args ...any) { got = fmt.Sprintf(format, args...) }

	report("session cleanup", func() { panic("boom") }, capture)

	if !strings.Contains(got, "session cleanup") {
		t.Errorf("log %q does not name the context", got)
	}
	if !strings.Contains(got, "boom") {
		t.Errorf("log %q does not carry the panic value", got)
	}
	// A panic with no stack is nearly useless for finding the cause.
	if !strings.Contains(got, "goroutine") {
		t.Errorf("log %q carries no stack trace", got)
	}
}

func TestDoLogsNothingWhenFnSucceeds(t *testing.T) {
	t.Parallel()
	logged := false
	report("ctx", func() {}, func(string, ...any) { logged = true })
	if logged {
		t.Error("a successful call logged something")
	}
}

// A loop is the reason this exists: one bad iteration must not end the loop.
func TestDoLetsALoopContinueAfterAPanic(t *testing.T) {
	t.Parallel()
	completed := 0
	for i := 0; i < 5; i++ {
		report("tick", func() {
			if i == 2 {
				panic("bad iteration")
			}
			completed++
		}, discard)
	}
	if completed != 4 {
		t.Errorf("completed = %d, want 4 (one iteration panicked, the rest must run)", completed)
	}
}

func TestDoIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report("concurrent", func() { panic("boom") }, discard)
		}()
	}
	wg.Wait()
}

// A nil panic value still unwinds the stack, and recover() returns nil for it,
// so it cannot be caught. Pinning the behaviour rather than the wish.
func TestDoPropagatesRuntimeErrors(t *testing.T) {
	t.Parallel()
	var got string
	capture := func(format string, args ...any) { got = fmt.Sprintf(format, args...) }

	var m map[string]string
	report("nil map write", func() { m["k"] = "v" }, capture)

	if !strings.Contains(got, "nil map") {
		t.Errorf("log %q does not describe the runtime error", got)
	}
}
