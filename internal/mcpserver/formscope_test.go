package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Words that appear in exactly one form's submissions, so "did the other form
// leak" is a substring search rather than a field-by-field comparison — the
// shape that finally worked for the withheld address.
const (
	mineWord   = "quarklight"
	theirsWord = "zephyrine"
)

func (h *harness) seedTwoForms(t *testing.T) {
	t.Helper()
	for _, f := range []store.Form{
		{ID: "mine", Name: "Mine", EmailTo: "me@example.com"},
		{ID: "theirs", Name: "Theirs Secret Client", EmailTo: "them@example.com"},
	} {
		if err := h.store.CreateForm(f); err != nil {
			t.Fatalf("CreateForm(%s): %v", f.ID, err)
		}
	}
	base := time.Now().UTC().Truncate(time.Second)
	// Each mutating tool gets its own victim, because the tools are called in
	// the order the listing comes back in and delete_submission sorts before
	// get_submission.
	for i, spec := range []struct{ id, form, word string }{
		{"m1", "mine", mineWord},
		{"mspam", "mine", mineWord},
		{"mdel", "mine", mineWord},
		{"t1", "theirs", theirsWord},
	} {
		raw, err := json.Marshal(map[string]string{"message": "about " + spec.word})
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		if err := h.store.CreateSubmission(store.Submission{
			ID: spec.id, FormID: spec.form, RawData: string(raw),
			CreatedAt: base.Add(-time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", spec.id, err)
		}
		heldRaw, err := json.Marshal(map[string]string{"message": "held " + spec.word})
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		if err := h.store.CreateHeldSubmission(
			store.Submission{ID: spec.id + "h", FormID: spec.form,
				RawData: string(heldRaw), CreatedAt: base},
			9, 6, []store.SpamSignal{{Check: "markup", Field: "message", Match: "x", Weight: 9}},
		); err != nil {
			t.Fatalf("CreateHeldSubmission(%sh): %v", spec.id, err)
		}
	}
}

// scopedToolArgs is a valid call for every tool a scoped read+write+delete token
// is offered, against the seed above.
var scopedToolArgs = map[string]map[string]any{
	"list_forms":         nil,
	"list_submissions":   {"status": "all"},
	"get_submission":     {"submission_id": "m1"},
	"search_submissions": {"query": theirsWord},
	"list_quarantine":    nil,
	"get_stats":          nil,
	"mark_read":          {"submission_id": "m1"},
	"mark_all_read":      {"form_id": "mine"},
	"mark_spam":          {"submission_id": "mspam"},
	"delete_submission":  {"submission_id": "mdel"},
	"delete_quarantined": {"submission_ids": []any{"mdelh"}},
}

// TestAScopedTokenNeverSeesAnotherForm is the property the whole feature is
// for, and it is derived rather than enumerated: every tool the token is
// offered gets called, and the other form's id, name and vocabulary must appear
// in none of the answers. A tool added later is covered without anyone
// remembering to add a case to a list.
func TestAScopedTokenNeverSeesAnotherForm(t *testing.T) {
	t.Parallel()

	h := newHarnessScoped(t, []string{"mine"}, "read", "write", "delete")
	h.seedTwoForms(t)
	session := h.connect(t)

	names := toolNames(t, session)
	for _, name := range names {
		if _, ok := scopedToolArgs[name]; !ok {
			t.Fatalf("no arguments recorded for %q — add it to scopedToolArgs", name)
		}
	}
	if len(names) != len(scopedToolArgs) {
		t.Fatalf("a scoped token sees %d tools but scopedToolArgs has %d: %v",
			len(names), len(scopedToolArgs), names)
	}

	var sawMine bool
	for _, name := range names {
		res := call(t, session, name, scopedToolArgs[name])
		if res.IsError {
			t.Errorf("%s: %s", name, resultText(res))
			continue
		}
		body := wireText(t, res.StructuredContent) + resultText(res)
		for _, leak := range []string{theirsWord, "Theirs Secret Client", `"theirs"`, `"t1"`, `"t1h"`} {
			if strings.Contains(body, leak) {
				t.Errorf("%s leaked %q from the other form:\n%s", name, leak, body)
			}
		}
		if strings.Contains(body, mineWord) || strings.Contains(body, `"mine"`) {
			sawMine = true
		}
	}

	// Without this the test passes if every tool simply returned nothing.
	if !sawMine {
		t.Error("no tool returned anything from the form in scope, so nothing was inspected")
	}
}

// TestAScopedTokenIsNotOfferedTheInstanceWideTools. A block rule added by a
// one-form token applies to every form, and the rule list is the operator's own
// configuration. Both are the bound escaping through the config rather than
// through the data.
func TestAScopedTokenIsNotOfferedTheInstanceWideTools(t *testing.T) {
	t.Parallel()

	instanceWide := []string{"list_filter_rules", "add_block_rule"}

	t.Run("absent from the listing", func(t *testing.T) {
		t.Parallel()
		h := newHarnessScoped(t, []string{"mine"}, "read", "write")
		session := h.connect(t)
		names := toolNames(t, session)
		for _, name := range instanceWide {
			if slices.Contains(names, name) {
				t.Errorf("a scoped token is offered %q", name)
			}
		}
	})

	t.Run("refused when called anyway", func(t *testing.T) {
		t.Parallel()
		h := newHarnessScoped(t, []string{"mine"}, "read", "write")
		h.seedTwoForms(t)
		session := h.connect(t)
		for _, name := range instanceWide {
			args := map[string]any{}
			if name == "add_block_rule" {
				args = map[string]any{"type": "email", "value": "x@example.invalid"}
			}
			// Unregistered, so the SDK refuses at the transport rather than
			// running a handler that returns an error — which is the stronger
			// of the two answers, and why this asserts on the call and not on
			// the result. requireAllForms is the second gate, and it is
			// exercised directly below, since no session can reach a handler
			// its own server does not carry.
			res, err := session.CallTool(context.Background(),
				&mcp.CallToolParams{Name: name, Arguments: args})
			if err == nil && !res.IsError {
				t.Errorf("%s succeeded for a scoped token", name)
			}
		}
	})

	t.Run("an unscoped token still has them", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "read", "write")
		names := toolNames(t, h.connect(t))
		for _, name := range instanceWide {
			if !slices.Contains(names, name) {
				t.Errorf("an unscoped token lost %q", name)
			}
		}
	})
}

// TestAnOutOfScopeSubmissionIsIndistinguishableFromAMissingOne. Answering
// "forbidden" would confirm the id exists and belongs to a form the caller
// cannot see, which is the same distinction the endpoint's 401s deliberately
// refuse to draw between unknown, revoked and expired tokens.
func TestAnOutOfScopeSubmissionIsIndistinguishableFromAMissingOne(t *testing.T) {
	t.Parallel()

	h := newHarnessScoped(t, []string{"mine"}, "read", "write", "delete")
	h.seedTwoForms(t)
	session := h.connect(t)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"get_submission", map[string]any{"submission_id": "t1"}},
		{"mark_read", map[string]any{"submission_id": "t1"}},
		{"mark_spam", map[string]any{"submission_id": "t1"}},
		{"delete_submission", map[string]any{"submission_id": "t1"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			outOfScope := call(t, session, tc.tool, tc.args)
			if !outOfScope.IsError {
				t.Fatalf("%s reached another form's submission", tc.tool)
			}
			missing := call(t, session, tc.tool, map[string]any{"submission_id": "no-such-id"})
			if !missing.IsError {
				t.Fatalf("%s accepted an id that does not exist", tc.tool)
			}
			// The same sentence with each id substituted. Comparing the strings
			// outright would compare the ids, which of course differ; what must
			// match is the answer's shape, since anything else tells a caller
			// whether the id it guessed is real.
			for _, c := range []struct {
				res *mcp.CallToolResult
				id  string
			}{{outOfScope, tc.args["submission_id"].(string)}, {missing, "no-such-id"}} {
				want := fmt.Sprintf("no submission with id %q", c.id)
				if got := resultText(c.res); got != want {
					t.Errorf("answer for %s was %q, want %q — the two cases must be "+
						"indistinguishable", c.id, got, want)
				}
			}
		})
	}

	// The other form's submission is still there afterwards.
	if _, err := h.store.GetSubmission("t1"); err != nil {
		t.Errorf("the out-of-scope submission was changed or destroyed: %v", err)
	}
}

func TestMarkAllReadRefusesAFormOutOfScope(t *testing.T) {
	t.Parallel()

	h := newHarnessScoped(t, []string{"mine"}, "read", "write")
	h.seedTwoForms(t)
	session := h.connect(t)

	if res := call(t, session, "mark_all_read", map[string]any{"form_id": "theirs"}); !res.IsError {
		t.Error("a scoped token marked another form's inbox read")
	}
	sub, err := h.store.GetSubmission("t1")
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.Read {
		t.Error("the other form's submission was marked read")
	}
}

func TestDeleteQuarantinedCannotReachAnotherForm(t *testing.T) {
	t.Parallel()

	h := newHarnessScoped(t, []string{"mine"}, "read", "delete")
	h.seedTwoForms(t)
	session := h.connect(t)

	out := decode[deleteCountOut](t, call(t, session, "delete_quarantined",
		map[string]any{"submission_ids": []any{"m1h", "t1h"}}))
	if out.Deleted != 1 {
		t.Errorf("deleted = %d, want 1 — the out-of-scope id matches nothing", out.Deleted)
	}
	if _, err := h.store.GetHeldSubmission("t1h"); err != nil {
		t.Errorf("the other form's quarantined submission went: %v", err)
	}
}

// TestGetStatsWithholdsTheWaitlistWhenScoped. Waitlist entries belong to no
// form, so a scoped call cannot answer that question and must not report a zero
// it did not measure.
func TestGetStatsWithholdsTheWaitlistWhenScoped(t *testing.T) {
	t.Parallel()

	scoped := newHarnessScoped(t, []string{"mine"}, "read")
	scoped.seedTwoForms(t)
	out := decode[statsOut](t, call(t, scoped.connect(t), "get_stats", nil))
	if !out.WaitlistWithheld {
		t.Error("a scoped get_stats did not say the waitlist figure is unavailable")
	}
	if out.WaitlistEntries != 0 {
		t.Errorf("waitlist_entries = %d, want it absent", out.WaitlistEntries)
	}

	all := newHarness(t, "read")
	all.seedTwoForms(t)
	full := decode[statsOut](t, call(t, all.connect(t), "get_stats", nil))
	if full.WaitlistWithheld {
		t.Error("an unscoped get_stats withheld the waitlist figure")
	}
}

// TestAnUnscopedTokenStillSeesEveryForm is the other direction, and the upgrade
// path: every token minted before this existed carries no forms and must keep
// the access it has.
func TestAnUnscopedTokenStillSeesEveryForm(t *testing.T) {
	t.Parallel()

	h := newHarness(t, "read")
	h.seedTwoForms(t)
	session := h.connect(t)

	out := decode[listFormsOut](t, call(t, session, "list_forms", nil))
	if len(out.Forms) != 2 {
		t.Errorf("forms = %+v, want both", out.Forms)
	}

	subs := decode[listSubmissionsOut](t, call(t, session, "list_submissions", map[string]any{"status": "all"}))
	seen := map[string]bool{}
	for _, sub := range subs.Submissions {
		seen[sub.FormID] = true
	}
	if !seen["mine"] || !seen["theirs"] {
		t.Errorf("saw forms %v, want both", seen)
	}
}

// TestATokenWithNoFormAccessReachesNothing. FormAccess absent from Extra is the
// bug case — a verifier that forgot to set it — and it must deny rather than
// grant, which is the direction ParseScopes takes for the same reason.
func TestATokenWithNoFormAccessReachesNothing(t *testing.T) {
	t.Parallel()

	h := newHarnessFull(t, Options{}, "test-token", FormAccess{}, "read")
	h.seedTwoForms(t)
	session := h.connect(t)

	out := decode[listSubmissionsOut](t, call(t, session, "list_submissions", map[string]any{"status": "all"}))
	if len(out.Submissions) != 0 {
		t.Errorf("a token with no form access read %d submissions", len(out.Submissions))
	}
	forms := decode[listFormsOut](t, call(t, session, "list_forms", nil))
	if len(forms.Forms) != 0 {
		t.Errorf("a token with no form access listed %d forms", len(forms.Forms))
	}
}

// TestRequireAllFormsIsTheSecondGate. The registration above decides what a
// client is told about; this decides what would run. No session can reach a
// handler its own server does not carry, so the two cannot be checked the same
// way — but a wiring mistake in the first must still be caught by the second,
// which is exactly the arrangement requireScope already has.
func TestRequireAllFormsIsTheSecondGate(t *testing.T) {
	t.Parallel()

	withAccess := func(access any) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{
			TokenInfo: &auth.TokenInfo{Extra: map[string]any{FormsKey: access}},
		}}
	}

	if err := requireAllForms(withAccess(FormAccess{All: true})); err != nil {
		t.Errorf("an unbounded token was refused: %v", err)
	}
	for _, tc := range []struct {
		name   string
		access any
	}{
		{"bounded to a form", FormAccess{IDs: []string{"mine"}}},
		{"bounded to nothing", FormAccess{}},
		{"something else entirely", "all"},
	} {
		if err := requireAllForms(withAccess(tc.access)); err == nil {
			t.Errorf("%s was allowed to reach instance-wide state", tc.name)
		}
	}
	if err := requireAllForms(&mcp.CallToolRequest{}); err == nil {
		t.Error("a request with no token info was allowed to reach instance-wide state")
	}
}

// TestFormAccessDeniesWhatItCannotInterpret covers the case the test above
// cannot: an Extra map with no FormsKey, or something else under it.
//
// Passing FormAccess{} exercises a successful type assertion returning a zero
// value, which is a different path from the assertion failing — so a formAccess
// that fell back to "every form" on a missing key passed that test while
// granting everything on exactly the wiring mistake this is here to catch.
func TestFormAccessDeniesWhatItCannotInterpret(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"no key at all", map[string]any{}},
		{"a nil map", nil},
		{"only the token name", map[string]any{TokenNameKey: "laptop"}},
		{"a string under the key", map[string]any{FormsKey: "all"}},
		{"a bare list under the key", map[string]any{FormsKey: []string{"mine"}}},
		{"a pointer under the key", map[string]any{FormsKey: &FormAccess{All: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formAccess(tc.extra)
			if got.All || len(got.IDs) > 0 {
				t.Errorf("formAccess = %+v, want nothing granted", got)
			}
			if got.Scope().Allows("mine") {
				t.Error("the resulting scope reaches a form")
			}
		})
	}

	if got := formAccess(map[string]any{FormsKey: FormAccess{All: true}}); !got.All {
		t.Error("a well-formed value was not read back")
	}
}
