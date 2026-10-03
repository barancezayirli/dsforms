package mcpserver

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/store"
)

func heldIDs(t *testing.T, h *harness) []string {
	t.Helper()
	subs, err := h.store.HeldSubmissions(store.AllForms(), 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range subs {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestEmptyQuarantineIsOfferedOnlyWithDelete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scopes []string
		want   bool
	}{
		{[]string{"read", "write"}, false},
		{[]string{"read", "write", "delete"}, true},
	} {
		h := newHarness(t, tc.scopes...)
		got := slices.Contains(toolNames(t, h.connect(t)), "empty_quarantine")
		if got != tc.want {
			t.Errorf("scopes %v: offered = %v, want %v", tc.scopes, got, tc.want)
		}
	}
}

// Never called blind: the count is what makes it delete only what was seen.
func TestEmptyQuarantineRefusesAMissingCount(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	h.seed(t)
	session := h.connect(t)

	for _, args := range []map[string]any{nil, {"expected_count": 0}, {"expected_count": -1}} {
		if res := call(t, session, "empty_quarantine", args); !res.IsError {
			t.Errorf("args %v were accepted", args)
		}
	}
	if len(heldIDs(t, h)) == 0 {
		t.Fatal("the quarantine was emptied without a count")
	}
}

func TestEmptyQuarantineClearsEverythingHeldAndNothingElse(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	h.seed(t)
	session := h.connect(t)

	held := len(heldIDs(t, h))
	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": held}))
	if !out.OK || out.Deleted != held {
		t.Fatalf("out = %+v, want %d deleted", out, held)
	}
	if left := heldIDs(t, h); len(left) != 0 {
		t.Errorf("still held: %v", left)
	}
	for _, id := range []string{"s1", "s2"} {
		if _, err := h.store.GetSubmission(id); err != nil {
			t.Errorf("inbox submission %s was deleted: %v", id, err)
		}
	}
}

// A form-bound token clears its own forms only, and counts only what it can
// see — so a count that includes another form's spam is a mismatch.
func TestEmptyQuarantineStaysInsideTheTokensForms(t *testing.T) {
	t.Parallel()
	h := newHarnessScoped(t, []string{"mine"}, "read", "delete")
	h.seedTwoForms(t)
	session := h.connect(t)

	// Four are held in total; this token sees three.
	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 4}))
	if out.OK || out.Deleted != 0 {
		t.Fatalf("a count including another form's spam was accepted: %+v", out)
	}

	out = decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 3}))
	if !out.OK || out.Deleted != 3 {
		t.Fatalf("out = %+v, want 3 deleted", out)
	}
	if left := heldIDs(t, h); !slices.Equal(left, []string{"t1h"}) {
		t.Errorf("held after clearing = %v, want only the other form's", left)
	}
	if strings.Contains(out.Message, "Theirs") {
		t.Errorf("the message names a form the token cannot see: %q", out.Message)
	}
}

// A form outside the token's bound answers exactly as one that does not
// exist, as everywhere else: "forbidden" would confirm it is real.
func TestEmptyQuarantineFormIDOutsideTheBoundIsUnknown(t *testing.T) {
	t.Parallel()
	h := newHarnessScoped(t, []string{"mine"}, "read", "delete")
	h.seedTwoForms(t)
	session := h.connect(t)

	outside := resultText(call(t, session, "empty_quarantine", map[string]any{"expected_count": 1, "form_id": "theirs"}))
	unknown := resultText(call(t, session, "empty_quarantine", map[string]any{"expected_count": 1, "form_id": "nope"}))
	if !strings.Contains(outside, "no form with id") || strings.Replace(outside, "theirs", "nope", 1) != unknown {
		t.Errorf("outside the bound: %q\nunknown: %q\nwant the same answer", outside, unknown)
	}
	if _, err := h.store.GetHeldSubmission("t1h"); err != nil {
		t.Errorf("the other form's spam went: %v", err)
	}

	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 3, "form_id": "mine"}))
	if !out.OK || out.Deleted != 3 {
		t.Errorf("clearing the token's own form: %+v", out)
	}
}

func TestEmptyQuarantineOlderThanDays(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	h.seed(t)
	if err := h.store.CreateHeldSubmission(store.Submission{
		ID: "oldspam", FormID: "contact", RawData: `{"message":"old"}`,
		CreatedAt: time.Now().UTC().Add(-40 * 24 * time.Hour),
	}, 8, 6, nil); err != nil {
		t.Fatal(err)
	}
	// An inbox message from 40 days ago, marked as spam just now. It has been
	// in quarantine for seconds, so "older than 30 days" must not reach it.
	if err := h.store.CreateSubmission(store.Submission{
		ID: "oldreal", FormID: "contact", RawData: `{"message":"hello"}`,
		CreatedAt: time.Now().UTC().Add(-40 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.MarkSpam("oldreal", "admin"); err != nil {
		t.Fatal(err)
	}
	session := h.connect(t)

	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 1, "older_than_days": 30}))
	if !out.OK || out.Deleted != 1 {
		t.Fatalf("out = %+v, want the one old message deleted", out)
	}
	left := heldIDs(t, h)
	if slices.Contains(left, "oldspam") || !slices.Contains(left, "spam1") {
		t.Errorf("held after clearing old spam = %v, want spam1 kept and oldspam gone", left)
	}
	if !slices.Contains(left, "oldreal") {
		t.Errorf("held = %v; a message marked as spam just now was deleted as old", left)
	}

	// 0 is the same as leaving it out: every age.
	for _, days := range []int{-3, 366} {
		if res := call(t, session, "empty_quarantine", map[string]any{"expected_count": 1, "older_than_days": days}); !res.IsError {
			t.Errorf("older_than_days %d was accepted", days)
		}
	}
}

// A mismatch deletes nothing, and says what the real count is, so the client
// can look again rather than guess.
func TestEmptyQuarantineMismatchDeletesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	h.seed(t)
	session := h.connect(t)
	before := heldIDs(t, h)

	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": len(before) + 5}))
	if out.OK || out.Deleted != 0 {
		t.Fatalf("a wrong count deleted something: %+v", out)
	}
	if !strings.Contains(out.Message, "holds 1") || !strings.Contains(out.Message, "list_quarantine") {
		t.Errorf("message %q should give the real count and say how to look again", out.Message)
	}
	if after := heldIDs(t, h); !slices.Equal(before, after) {
		t.Errorf("held before %v, after %v", before, after)
	}
}
