package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	// A client that read "total: 0" and passed it on is told there is nothing
	// to delete, not sent back to the tool that gave it the 0.
	msg := resultText(call(t, session, "empty_quarantine", map[string]any{"expected_count": 0}))
	if !strings.Contains(msg, "nothing to delete") {
		t.Errorf("message for a zero count = %q", msg)
	}
}

// expected_count that is not a whole number never reaches the delete.
func TestEmptyQuarantineRefusesAMalformedCount(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	h.seed(t)
	session := h.connect(t)
	before := heldIDs(t, h)

	for _, bad := range []any{"1", 1.5, 1e30} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "empty_quarantine", Arguments: map[string]any{"expected_count": bad},
		})
		if err == nil && !res.IsError {
			t.Errorf("expected_count %v (%T) was accepted", bad, bad)
		}
	}
	if after := heldIDs(t, h); !slices.Equal(before, after) {
		t.Errorf("held before %v, after %v", before, after)
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
	// The refusal counts only what this token can see: "holds 4" would tell it
	// another form has spam in quarantine.
	if !strings.Contains(out.Message, "holds 3 ") {
		t.Errorf("mismatch message = %q, want it to count the token's own 3", out.Message)
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

	// Out of range is refused.
	for _, days := range []int{-3, 366} {
		if res := call(t, session, "empty_quarantine", map[string]any{"expected_count": 1, "older_than_days": days}); !res.IsError {
			t.Errorf("older_than_days %d was accepted", days)
		}
	}
	// The ends of the range are accepted: a count that cannot match answers
	// ok:false, which is the handler having run, not a refusal of the input.
	for _, days := range []int{1, 365} {
		if res := call(t, session, "empty_quarantine", map[string]any{"expected_count": 999, "older_than_days": days}); res.IsError {
			t.Errorf("older_than_days %d was refused: %s", days, resultText(res))
		}
	}
	// 0 is the same as leaving it out: every age. Two are held now.
	out = decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 2, "older_than_days": 0}))
	if !out.OK || out.Deleted != 2 || len(heldIDs(t, h)) != 0 {
		t.Errorf("older_than_days 0: %+v, held left %v; want everything cleared", out, heldIDs(t, h))
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
	for _, want := range []string{"Nothing was deleted", "holds 1 ", "not 6", "every form this token reaches", "list_quarantine"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("message %q does not contain %q", out.Message, want)
		}
	}
	if after := heldIDs(t, h); !slices.Equal(before, after) {
		t.Errorf("held before %v, after %v", before, after)
	}
}

// form_id narrows an unbounded token. With a token already bound to that form
// the narrowing would be invisible, so this uses one that reaches every form.
func TestEmptyQuarantineFormIDNarrowsAnUnboundedToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	h.seedTwoForms(t)
	session := h.connect(t)

	// Four are held in all, three of them in "mine".
	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 4, "form_id": "mine"}))
	if out.OK || out.Deleted != 0 || !strings.Contains(out.Message, "holds 3 ") {
		t.Fatalf("the whole instance's count was accepted for one form: %+v", out)
	}

	out = decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 3, "form_id": "mine"}))
	if !out.OK || out.Deleted != 3 {
		t.Fatalf("out = %+v, want 3 deleted", out)
	}
	if left := heldIDs(t, h); !slices.Equal(left, []string{"t1h"}) {
		t.Errorf("held after clearing one form = %v, want the other form's left", left)
	}
	if out.Message != "Deleted 3 quarantined submissions (Mine)." {
		t.Errorf("message = %q", out.Message)
	}
}

func TestEmptyQuarantineFormAndAgeTogether(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	for _, f := range []store.Form{{ID: "mine", Name: "Mine", EmailTo: "a@b.com"}, {ID: "theirs", Name: "Theirs", EmailTo: "a@b.com"}} {
		if err := h.store.CreateForm(f); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for id, at := range map[string]time.Time{
		"mine-old": now.Add(-40 * 24 * time.Hour), "mine-new": now,
		"theirs-old": now.Add(-40 * 24 * time.Hour), "theirs-new": now,
	} {
		form := strings.SplitN(id, "-", 2)[0]
		if err := h.store.CreateHeldSubmission(store.Submission{ID: id, FormID: form, RawData: `{"m":"x"}`, CreatedAt: at}, 8, 6, nil); err != nil {
			t.Fatal(err)
		}
	}
	session := h.connect(t)

	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{
		"expected_count": 1, "form_id": "mine", "older_than_days": 30,
	}))
	if !out.OK || out.Deleted != 1 {
		t.Fatalf("out = %+v, want only the named form's old message deleted", out)
	}
	if left := heldIDs(t, h); !slices.Equal(left, []string{"mine-new", "theirs-new", "theirs-old"}) {
		t.Errorf("held = %v", left)
	}
	if out.Message != "Deleted 1 quarantined submissions (Mine, in quarantine more than 30 days)." {
		t.Errorf("message = %q", out.Message)
	}
}

// list_quarantine takes the same filters, so a client can see and count what
// a clear will delete before it asks for it. Without this the only source of
// the count for a filtered clear is the refusal itself, and echoing that back
// deletes messages the client never looked at.
func TestListQuarantineCountsWhatEmptyQuarantineDeletes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "delete")
	for _, f := range []store.Form{{ID: "mine", Name: "Mine", EmailTo: "a@b.com"}, {ID: "theirs", Name: "Theirs", EmailTo: "a@b.com"}} {
		if err := h.store.CreateForm(f); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for id, at := range map[string]time.Time{
		"mine-old": now.Add(-40 * 24 * time.Hour), "mine-new": now,
		"theirs-old": now.Add(-40 * 24 * time.Hour), "theirs-new": now,
	} {
		form := strings.SplitN(id, "-", 2)[0]
		if err := h.store.CreateHeldSubmission(store.Submission{ID: id, FormID: form, RawData: `{"m":"x"}`, CreatedAt: at}, 8, 6, nil); err != nil {
			t.Fatal(err)
		}
	}
	session := h.connect(t)

	for _, tc := range []struct {
		name   string
		filter map[string]any
		want   []string
	}{
		{"no filter", map[string]any{}, []string{"mine-new", "mine-old", "theirs-new", "theirs-old"}},
		{"one form", map[string]any{"form_id": "mine"}, []string{"mine-new", "mine-old"}},
		{"older", map[string]any{"older_than_days": 30}, []string{"mine-old", "theirs-old"}},
		{"both", map[string]any{"form_id": "theirs", "older_than_days": 30}, []string{"theirs-old"}},
	} {
		listed := decode[listQuarantineOut](t, call(t, session, "list_quarantine", tc.filter))
		var ids []string
		for _, sub := range listed.Submissions {
			ids = append(ids, sub.ID)
			if sub.HeldAt == "" {
				t.Errorf("%s: %s carries no held_at", tc.name, sub.ID)
			}
		}
		slices.Sort(ids)
		if !slices.Equal(ids, tc.want) || listed.Total != len(tc.want) {
			t.Errorf("%s: listed %v, total %d; want %v", tc.name, ids, listed.Total, tc.want)
		}
	}

	// The workflow: list with a filter, pass its total, and exactly those go.
	filter := map[string]any{"form_id": "theirs", "older_than_days": 30}
	total := decode[listQuarantineOut](t, call(t, session, "list_quarantine", filter)).Total
	filter["expected_count"] = total
	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", filter))
	if !out.OK || out.Deleted != 1 {
		t.Fatalf("clearing what was listed: %+v", out)
	}
	if left := heldIDs(t, h); !slices.Equal(left, []string{"mine-new", "mine-old", "theirs-new"}) {
		t.Errorf("held = %v", left)
	}
}

// list_quarantine's form_id obeys the token's bound like every other tool: a
// form outside it answers as one that does not exist.
func TestListQuarantineFormIDOutsideTheBoundIsUnknown(t *testing.T) {
	t.Parallel()
	h := newHarnessScoped(t, []string{"mine"}, "read")
	h.seedTwoForms(t)
	session := h.connect(t)

	outside := resultText(call(t, session, "list_quarantine", map[string]any{"form_id": "theirs"}))
	unknown := resultText(call(t, session, "list_quarantine", map[string]any{"form_id": "nope"}))
	if !strings.Contains(outside, "no form with id") || strings.Replace(outside, "theirs", "nope", 1) != unknown {
		t.Errorf("outside the bound: %q\nunknown: %q\nwant the same answer", outside, unknown)
	}
	if res := call(t, session, "list_quarantine", map[string]any{"older_than_days": 366}); !res.IsError {
		t.Error("list_quarantine accepted older_than_days 366")
	}
}

// The age cutoff is taken from the server's clock, not the wall clock, so it
// can be tested without waiting: with the clock moved 100 days on, spam held
// today has been in quarantine for more than 30 days.
func TestEmptyQuarantineUsesTheInjectedClock(t *testing.T) {
	t.Parallel()
	future := time.Now().UTC().Add(100 * 24 * time.Hour)
	h := newHarnessOpts(t, Options{Now: func() time.Time { return future }}, "test-token", "read", "delete")
	h.seed(t)
	session := h.connect(t)

	out := decode[deleteCountOut](t, call(t, session, "empty_quarantine", map[string]any{"expected_count": 1, "older_than_days": 30}))
	if !out.OK || out.Deleted != 1 {
		t.Errorf("out = %+v; with the clock 100 days on, today's spam is older than 30 days", out)
	}
}
