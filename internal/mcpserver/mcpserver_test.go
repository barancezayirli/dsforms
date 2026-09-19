package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tests in this file drive the real protocol: a real SDK client, over HTTP,
// through the real bearer middleware, against a real SQLite store. There is no
// fake store and no hand-rolled JSON-RPC, because the things most likely to be
// wrong here — the scope gate, the tool schemas, the wire shapes — are exactly
// what a mock would assume rather than check.

const testToken = "dsf_test"

// bearer adds the Authorization header the middleware reads.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// harness is one MCP endpoint plus the store behind it.
type harness struct {
	store   *store.Store
	http    *httptest.Server
	adminID string
}

// newHarness stands up the endpoint with a token carrying exactly scopes, with
// submitter IPs withheld — the shipped default.
func newHarness(t *testing.T, scopes ...string) *harness {
	t.Helper()
	return newHarnessOpts(t, Options{}, "test-token", scopes...)
}

// newHarnessWithIPs is the same, for an instance that has opted into returning
// submitter IPs.
func newHarnessWithIPs(t *testing.T, scopes ...string) *harness {
	t.Helper()
	return newHarnessOpts(t, Options{IncludeIPs: true}, "test-token", scopes...)
}

// newHarnessNamed is the same with a named token, for the audit-trail tests.
func newHarnessNamed(t *testing.T, tokenName string, scopes ...string) *harness {
	t.Helper()
	return newHarnessOpts(t, Options{}, tokenName, scopes...)
}

func newHarnessOpts(t *testing.T, opts Options, tokenName string, scopes ...string) *harness {
	t.Helper()

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	admin, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}

	verify := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if token != testToken {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{
			Scopes: scopes, UserID: admin.ID,
			Extra: map[string]any{TokenNameKey: tokenName},
		}, nil
	}
	// AllowMissingExpiration mirrors the production wiring: dsforms tokens may
	// legitimately never expire, and without this the SDK rejects every one.
	mw := auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})

	ts := httptest.NewServer(mw(New(st, "test", opts).Handler()))
	t.Cleanup(ts.Close)

	return &harness{store: st, http: ts, adminID: admin.ID}
}

// connect opens an MCP session over the endpoint.
func (h *harness) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   h.http.URL,
		HTTPClient: &http.Client{Transport: bearer{token: testToken}},
		// The server is stateless, so it answers GET with 405 by design and
		// there is no standalone stream to open.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// toolNames lists what the session is told it can call.
func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// call invokes a tool and returns the result. A transport-level failure is
// fatal; a tool error is returned for the caller to assert on.
func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

// resultText flattens a tool result's content, for asserting on messages.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// decode reads a successful result's structured output.
func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned an error: %s", resultText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling structured content: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding structured content %s: %v", raw, err)
	}
	return out
}

// seed puts a form and some submissions in the store.
func (h *harness) seed(t *testing.T) {
	t.Helper()
	if err := h.store.CreateForm(store.Form{ID: "contact", Name: "Contact", EmailTo: "me@example.com"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	subs := []store.Submission{
		{ID: "s1", FormID: "contact", RawData: `{"name":"Ada","message":"hello"}`, IP: "198.51.100.1", CreatedAt: base.Add(-2 * time.Hour), SpamScore: 1, HeldThreshold: 6},
		{ID: "s2", FormID: "contact", RawData: `{"name":"Grace","message":"about the pricing"}`, IP: "198.51.100.2", CreatedAt: base.Add(-time.Hour), SpamScore: 2, HeldThreshold: 6},
	}
	for _, sub := range subs {
		if err := h.store.CreateSubmission(sub); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", sub.ID, err)
		}
	}
	if err := h.store.MarkRead("s1"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if err := h.store.CreateHeldSubmission(
		store.Submission{ID: "spam1", FormID: "contact", RawData: `{"message":"[url=http://x]buy[/url]"}`, CreatedAt: base},
		9, 6, []store.SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 9}},
	); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

// TestToolsListIsScopedToTheToken. A client shown a tool it will then be refused
// has been told two different things, and a model handed a delete tool it cannot
// call will spend its turn discovering that.
func TestToolsListIsScopedToTheToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		scopes        []string
		wantSome      []string
		wantNoneOf    []string
		wantEmptyList bool
	}{
		{
			name:       "read only",
			scopes:     []string{"read"},
			wantSome:   []string{"get_stats", "get_submission", "list_forms", "list_submissions"},
			wantNoneOf: []string{"mark_read", "mark_spam", "add_block_rule", "delete_submission", "delete_quarantined"},
		},
		{
			name:       "read and write",
			scopes:     []string{"read", "write"},
			wantSome:   []string{"list_submissions", "mark_spam", "mark_read", "add_block_rule"},
			wantNoneOf: []string{"delete_submission", "delete_quarantined"},
		},
		{
			name:     "everything",
			scopes:   []string{"read", "write", "delete"},
			wantSome: []string{"list_submissions", "mark_spam", "delete_submission", "delete_quarantined"},
		},
		{
			name:       "write without read",
			scopes:     []string{"write"},
			wantSome:   []string{"mark_spam"},
			wantNoneOf: []string{"list_submissions", "get_stats"},
		},
		{
			name:          "no scopes at all",
			scopes:        nil,
			wantEmptyList: true,
		},
		{
			name:          "only an unrecognised scope",
			scopes:        []string{"superuser"},
			wantEmptyList: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tt.scopes...)
			got := toolNames(t, h.connect(t))

			if tt.wantEmptyList {
				if len(got) != 0 {
					t.Fatalf("a token with scopes %v was offered %v; it must be offered nothing", tt.scopes, got)
				}
				return
			}
			for _, want := range tt.wantSome {
				if !slices.Contains(got, want) {
					t.Errorf("tool %q missing from %v", want, got)
				}
			}
			for _, banned := range tt.wantNoneOf {
				if slices.Contains(got, banned) {
					t.Errorf("tool %q offered to a token with scopes %v", banned, tt.scopes)
				}
			}
		})
	}
}

// TestAReadTokenCannotCallAWriteTool asserts the refusal at the *call*, which is
// the part that matters. Absence from tools/list is a courtesy; a client is free
// to call any name it likes.
func TestAReadTokenCannotCallAWriteTool(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)
	session := h.connect(t)

	for _, name := range []string{"mark_spam", "mark_read", "add_block_rule", "delete_submission", "delete_quarantined"} {
		t.Run(name, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      name,
				Arguments: map[string]any{"submission_id": "s1", "form_id": "contact", "type": "email", "value": "x@example.com", "submission_ids": []string{"spam1"}},
			})
			// Either shape is a refusal: the SDK reports an unregistered tool as
			// a protocol error, and the in-handler gate as a tool error. What
			// must never happen is a success.
			if err != nil {
				return
			}
			if !res.IsError {
				t.Fatalf("a read-only token successfully called %s: %s", name, resultText(res))
			}
		})
	}

	// And nothing was actually written.
	sub, err := h.store.GetSubmission("s1")
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.IsHeld {
		t.Error("s1 was quarantined by a read-only token")
	}
	rules, err := h.store.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("a read-only token added %d filter rules", len(rules))
	}
	held, err := h.store.GetHeldSubmission("spam1")
	if err != nil {
		t.Fatalf("the quarantined submission was deleted by a read-only token: %v", err)
	}
	if held.ID != "spam1" {
		t.Errorf("unexpected held submission %q", held.ID)
	}
}

// TestRequireScopeIsTheGateNotTheListing tests the in-handler check directly.
//
// It is the half of the defence that survives a wiring mistake in the other
// half — if serverFor ever returned the wrong prebuilt server, this is what
// still refuses — so it is worth a test that does not go through serverFor
// at all.
func TestRequireScopeIsTheGateNotTheListing(t *testing.T) {
	t.Parallel()

	withScopes := func(scopes ...string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Scopes: scopes}}}
	}

	tests := []struct {
		name    string
		req     *mcp.CallToolRequest
		want    Scope
		wantErr bool
	}{
		{"has it", withScopes("read"), ScopeRead, false},
		{"has it among others", withScopes("read", "delete"), ScopeDelete, false},
		{"does not have it", withScopes("read"), ScopeWrite, true},
		{"no scopes", withScopes(), ScopeRead, true},
		{"unrecognised scope only", withScopes("superuser"), ScopeRead, true},
		{"no token info", &mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}, ScopeRead, true},
		{"no extra at all", &mcp.CallToolRequest{}, ScopeRead, true},
		{"nil request", nil, ScopeRead, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := requireScope(tt.req, tt.want)
			if tt.wantErr && err == nil {
				t.Errorf("requireScope(%v) = nil, want a refusal", tt.want)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("requireScope(%v) = %v, want nil", tt.want, err)
			}
		})
	}
}

// TestEveryScopeSubsetHasAServer. A missing key makes serverFor return nil, which
// the SDK answers with a bare 400 — a valid token silently unable to do anything,
// with nothing in the response to say why.
func TestEveryScopeSubsetHasAServer(t *testing.T) {
	t.Parallel()
	s := New(nil, "test", Options{})

	want := 1
	for range AllScopes {
		want *= 2
	}
	if len(s.byScope) != want {
		t.Errorf("built %d servers for %d scopes, want %d (one per subset)", len(s.byScope), len(AllScopes), want)
	}
	// Spot-check the two ends, since those are the ones a subset generator that
	// silently drops a case is most likely to lose.
	for _, key := range []string{"", "read,write,delete"} {
		if _, ok := s.byScope[key]; !ok {
			t.Errorf("no server built for the scope set %q", key)
		}
	}
}

// TestServerForRefusesWithoutTokenInfo. Mounting this handler without its bearer
// middleware must not serve an unauthenticated client the whole tool set.
func TestServerForRefusesWithoutTokenInfo(t *testing.T) {
	t.Parallel()
	s := New(nil, "test", Options{})
	if got := s.serverFor(httptest.NewRequest("POST", "/mcp", nil)); got != nil {
		t.Fatal("serverFor returned a server for a request carrying no token info")
	}
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func TestListSubmissionsDefaultsToUnreadAndExcludesQuarantine(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)
	session := h.connect(t)

	tests := []struct {
		name   string
		args   map[string]any
		wantID []string
	}{
		{"default is unread", nil, []string{"s2"}},
		{"explicit unread", map[string]any{"status": "unread"}, []string{"s2"}},
		{"read only", map[string]any{"status": "read"}, []string{"s1"}},
		{"all", map[string]any{"status": "all"}, []string{"s2", "s1"}},
		{"scoped to a form", map[string]any{"form_id": "contact", "status": "all"}, []string{"s2", "s1"}},
		{"unknown form", map[string]any{"form_id": "nope", "status": "all"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := decode[listSubmissionsOut](t, call(t, session, "list_submissions", tt.args))

			var ids []string
			for _, sub := range out.Submissions {
				ids = append(ids, sub.ID)
				if sub.Held {
					t.Errorf("quarantined submission %s appeared in the inbox listing", sub.ID)
				}
				if sub.ID == "spam1" {
					t.Error("the quarantined submission leaked into list_submissions")
				}
			}
			if !slices.Equal(ids, tt.wantID) {
				t.Errorf("ids = %v, want %v", ids, tt.wantID)
			}
			if out.Count != len(out.Submissions) {
				t.Errorf("count = %d, want %d", out.Count, len(out.Submissions))
			}
		})
	}
}

// TestListSubmissionsRefusesAnUnknownStatus. Widening a typo to "all" would hand
// back the whole inbox to a client that asked for unread.
func TestListSubmissionsRefusesAnUnknownStatus(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)

	res := call(t, h.connect(t), "list_submissions", map[string]any{"status": "unraed"})
	if !res.IsError {
		t.Fatalf("an unknown status was accepted: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "unraed") {
		t.Errorf("the error does not name the bad value: %s", resultText(res))
	}
}

// TestListSubmissionsPopulatesTheWholeSubmission. A tool result that silently
// reports score 0 for a submission the database says scored 2 is the same defect
// as the partial reads AGENT.md §5 records, one layer further out.
func TestListSubmissionsPopulatesTheWholeSubmission(t *testing.T) {
	t.Parallel()
	// The IP-including harness, because this test is about every field arriving
	// rather than about which fields are shared: withholding is its own test.
	h := newHarnessWithIPs(t, "read")
	h.seed(t)

	out := decode[listSubmissionsOut](t, call(t, h.connect(t), "list_submissions", map[string]any{"status": "unread"}))
	if len(out.Submissions) != 1 {
		t.Fatalf("got %d submissions, want 1", len(out.Submissions))
	}
	got := out.Submissions[0]
	switch {
	case got.FormName != "Contact":
		t.Errorf("FormName = %q, want %q", got.FormName, "Contact")
	case got.Fields["name"] != "Grace":
		t.Errorf("Fields = %v, want the decoded submission body", got.Fields)
	case got.SpamScore != 2:
		t.Errorf("SpamScore = %d, want 2", got.SpamScore)
	case got.Threshold != 6:
		t.Errorf("Threshold = %d, want 6", got.Threshold)
	case got.IP != "198.51.100.2":
		t.Errorf("IP = %q, want the stored value", got.IP)
	case got.CreatedAt == "":
		t.Error("CreatedAt is empty")
	}
}

func TestListQuarantineCarriesTheBreakdown(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)

	out := decode[listQuarantineOut](t, call(t, h.connect(t), "list_quarantine", nil))
	if len(out.Submissions) != 1 {
		t.Fatalf("got %d quarantined, want 1", len(out.Submissions))
	}
	if out.Total != 1 {
		t.Errorf("Total = %d, want 1", out.Total)
	}
	got := out.Submissions[0]
	if !got.Held {
		t.Error("Held = false for a quarantined submission")
	}
	if len(got.Signals) != 1 || got.Signals[0].Check != "markup" {
		t.Fatalf("Signals = %+v, want the recorded markup hit", got.Signals)
	}
	if got.Signals[0].Weight != 9 {
		t.Errorf("Weight = %d, want 9", got.Signals[0].Weight)
	}
}

func TestGetSubmissionReportsWhyItWasHeld(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)
	session := h.connect(t)

	held := decode[getSubmissionOut](t, call(t, session, "get_submission", map[string]any{"submission_id": "spam1"}))
	if !held.Submission.Held {
		t.Error("Held = false for a quarantined submission")
	}
	if len(held.Signals) != 1 {
		t.Errorf("Signals = %+v, want one", held.Signals)
	}

	// The other half: a submission that was never held reports no signals, which
	// is how a reader tells "held then restored" from "simply passed".
	clean := decode[getSubmissionOut](t, call(t, session, "get_submission", map[string]any{"submission_id": "s1"}))
	if clean.Submission.Held {
		t.Error("Held = true for an accepted submission")
	}
	if len(clean.Signals) != 0 {
		t.Errorf("Signals = %+v for a submission that was never held, want none", clean.Signals)
	}

	missing := call(t, session, "get_submission", map[string]any{"submission_id": "nope"})
	if !missing.IsError {
		t.Error("get_submission on a missing id succeeded")
	}
}

func TestGetStatsReportsTheDatabase(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)

	out := decode[statsOut](t, call(t, h.connect(t), "get_stats", nil))
	if out.Unread != 1 {
		t.Errorf("Unread = %d, want 1", out.Unread)
	}
	if out.Quarantined != 1 {
		t.Errorf("Quarantined = %d, want 1", out.Quarantined)
	}
	if out.TotalAccepted != 2 {
		t.Errorf("TotalAccepted = %d, want 2", out.TotalAccepted)
	}
	if out.Days != 7 {
		t.Errorf("Days = %d, want the default of 7", out.Days)
	}
	if len(out.PerForm) != 1 || out.PerForm[0].FormID != "contact" {
		t.Errorf("PerForm = %+v, want one entry for contact", out.PerForm)
	}
	if len(out.PerDay) == 0 {
		t.Error("PerDay is empty")
	}
}

// TestGetStatsClampsTheWindow. An unbounded day count is an unbounded scan and an
// unbounded result, on an endpoint whose caller is a language model.
func TestGetStatsClampsTheWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)
	session := h.connect(t)

	for _, tc := range []struct{ in, want int }{{0, 7}, {-5, 7}, {3, 3}, {9000, 90}} {
		out := decode[statsOut](t, call(t, session, "get_stats", map[string]any{"days": tc.in}))
		if out.Days != tc.want {
			t.Errorf("days %d clamped to %d, want %d", tc.in, out.Days, tc.want)
		}
	}
}

func TestListFormsCountsTheInbox(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)

	out := decode[listFormsOut](t, call(t, h.connect(t), "list_forms", nil))
	if len(out.Forms) != 1 {
		t.Fatalf("got %d forms, want 1", len(out.Forms))
	}
	f := out.Forms[0]
	switch {
	case f.ID != "contact":
		t.Errorf("ID = %q, want contact", f.ID)
	case f.Unread != 1:
		t.Errorf("Unread = %d, want 1", f.Unread)
	case f.Held != 1:
		t.Errorf("Held = %d, want 1", f.Held)
	case f.Received != 2:
		t.Errorf("Received = %d, want 2 accepted", f.Received)
	case f.SubmitURL != "/f/contact":
		t.Errorf("SubmitURL = %q, want /f/contact", f.SubmitURL)
	}
}

func TestSearchFindsSubmissions(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)
	session := h.connect(t)

	out := decode[listSubmissionsOut](t, call(t, session, "search_submissions", map[string]any{"query": "pricing"}))
	if len(out.Submissions) != 1 || out.Submissions[0].ID != "s2" {
		t.Errorf("search for \"pricing\" returned %+v, want s2", out.Submissions)
	}

	if res := call(t, session, "search_submissions", map[string]any{"query": "   "}); !res.IsError {
		t.Error("an empty query was accepted")
	}
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// TestMarkSpamThroughTheProtocol is the headline capability, asserted end to end
// and then against the database rather than against the tool's own message.
func TestMarkSpamThroughTheProtocol(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write")
	h.seed(t)
	session := h.connect(t)

	out := decode[markSpamOut](t, call(t, session, "mark_spam", map[string]any{"submission_id": "s2"}))
	if !out.OK {
		t.Error("OK = false")
	}
	if !out.Submission.Held {
		t.Error("the returned submission is not held")
	}

	held, err := h.store.GetHeldSubmission("s2")
	if err != nil {
		t.Fatalf("s2 is not in quarantine: %v", err)
	}
	if !held.Notified {
		t.Error("notified was cleared; restoring this would send a duplicate notification")
	}
	if held.SpamScore != 2 {
		t.Errorf("SpamScore = %d, want the original 2", held.SpamScore)
	}

	// The record of who did it, which is the whole reason this writes a signal.
	signals, err := h.store.SubmissionSignals("s2")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(signals) != 1 || signals[0].Check != "manual" {
		t.Fatalf("signals = %+v, want one manual signal", signals)
	}
	// The user *and* the token that acted — see TestActorNamesTheTokenNotJustTheUser.
	if signals[0].Match != "admin (test-token)" {
		t.Errorf("Match = %q, want the acting user and token", signals[0].Match)
	}

	// A retry says so plainly rather than failing in a way that invites the
	// client to try something else.
	again := call(t, session, "mark_spam", map[string]any{"submission_id": "s2"})
	if !again.IsError {
		t.Fatal("a second mark_spam reported success")
	}
	if !strings.Contains(resultText(again), "already in quarantine") {
		t.Errorf("a repeat call said %q, want it to say the submission is already quarantined", resultText(again))
	}

	missing := call(t, session, "mark_spam", map[string]any{"submission_id": "nope"})
	if !missing.IsError || !strings.Contains(resultText(missing), "no submission") {
		t.Errorf("mark_spam on a missing id said %q", resultText(missing))
	}
}

func TestMarkReadAndBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write")
	h.seed(t)
	session := h.connect(t)

	if res := call(t, session, "mark_read", map[string]any{"submission_id": "s2"}); res.IsError {
		t.Fatalf("mark_read: %s", resultText(res))
	}
	if sub, _ := h.store.GetSubmission("s2"); !sub.Read {
		t.Error("s2 is not read")
	}

	// read=false is the explicit undo, and it has to be distinguishable from the
	// field being omitted — which is why the input field is a pointer.
	if res := call(t, session, "mark_read", map[string]any{"submission_id": "s2", "read": false}); res.IsError {
		t.Fatalf("mark_read read=false: %s", resultText(res))
	}
	if sub, _ := h.store.GetSubmission("s2"); sub.Read {
		t.Error("s2 is still read after read=false")
	}

	if res := call(t, session, "mark_read", map[string]any{"submission_id": "nope"}); !res.IsError {
		t.Error("mark_read on a missing submission reported success")
	}
}

func TestMarkAllRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write")
	h.seed(t)
	session := h.connect(t)

	if res := call(t, session, "mark_all_read", map[string]any{"form_id": "contact"}); res.IsError {
		t.Fatalf("mark_all_read: %s", resultText(res))
	}
	n, err := h.store.UnreadCount("contact")
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if n != 0 {
		t.Errorf("UnreadCount = %d, want 0", n)
	}
	// Quarantine is not an inbox and must not be swept into one.
	if held, err := h.store.GetHeldSubmission("spam1"); err != nil || held.Read {
		t.Errorf("the quarantined submission was marked read (err = %v)", err)
	}

	for _, args := range []map[string]any{{"form_id": ""}, {"form_id": "nope"}} {
		if res := call(t, session, "mark_all_read", args); !res.IsError {
			t.Errorf("mark_all_read(%v) reported success", args)
		}
	}
}

// TestAddBlockRuleCannotCreateAnAllowRule is a security test, not a validation
// test.
//
// An allow rule skips the block list and all scoring, and an IP or CIDR allow
// rule turns one forgeable X-Forwarded-For header into a filter bypass — the
// recorded risk in AGENT.md §6. The kind is a constant at the call site rather
// than an input, so there is no string a client can send to reach it.
func TestAddBlockRuleCannotCreateAnAllowRule(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write")
	h.seed(t)
	session := h.connect(t)

	out := decode[addBlockRuleOut](t, call(t, session, "add_block_rule", map[string]any{
		"type": "email", "value": "spammer@example.com", "note": "from mcp",
	}))
	if out.Rule.Kind != "block" {
		t.Errorf("Kind = %q, want block", out.Rule.Kind)
	}

	// Every shape of "please make it an allow rule" a client could try.
	for _, args := range []map[string]any{
		{"type": "allow", "value": "friend@example.com"},
		{"type": "email", "value": "friend@example.com", "kind": "allow"},
		{"type": "ip", "value": "203.0.113.5", "kind": "allow"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "add_block_rule", Arguments: args})
		if err != nil {
			continue // rejected by the input schema, which is also a refusal
		}
		_ = res
	}

	rules, err := h.store.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	for _, r := range rules {
		if r.Kind != "block" {
			t.Errorf("an allow rule reached the database through the MCP surface: %+v", r)
		}
	}
}

func TestAddBlockRuleReportsValidationProblems(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write")
	h.seed(t)
	session := h.connect(t)

	for _, args := range []map[string]any{
		{"type": "nonsense", "value": "x"},
		{"type": "email", "value": "not-an-email"},
		{"type": "cidr", "value": "not-a-cidr"},
	} {
		if res := call(t, session, "add_block_rule", args); !res.IsError {
			t.Errorf("add_block_rule(%v) reported success", args)
		}
	}
}

// ---------------------------------------------------------------------------
// Deletes
// ---------------------------------------------------------------------------

func TestDeleteSubmission(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write", "delete")
	h.seed(t)
	session := h.connect(t)

	if res := call(t, session, "delete_submission", map[string]any{"submission_id": "s1"}); res.IsError {
		t.Fatalf("delete_submission: %s", resultText(res))
	}
	if _, err := h.store.GetSubmission("s1"); err == nil {
		t.Error("s1 still exists")
	}

	// A second delete is a clear "no such submission", not a silent success —
	// without the read-first there is no count to tell the two apart.
	again := call(t, session, "delete_submission", map[string]any{"submission_id": "s1"})
	if !again.IsError {
		t.Error("deleting a missing submission reported success")
	}
}

// TestDeleteQuarantinedRefusesAnEmptyList. An empty array from a client that
// meant to build one is the likeliest accidental request for a mass delete, so
// it is refused rather than interpreted.
func TestDeleteQuarantinedRefusesAnEmptyList(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write", "delete")
	h.seed(t)
	session := h.connect(t)

	if res := call(t, session, "delete_quarantined", map[string]any{"submission_ids": []string{}}); !res.IsError {
		t.Error("an empty id list was accepted")
	}
	if _, err := h.store.GetHeldSubmission("spam1"); err != nil {
		t.Errorf("the quarantine was emptied by an empty id list: %v", err)
	}
}

// TestDeleteQuarantinedCannotReachTheInbox. DeleteHeld scopes its statement to
// held rows; this asserts that scoping survives the tool, because an id-only
// delete here would let a "clean up spam" call destroy real submissions.
func TestDeleteQuarantinedCannotReachTheInbox(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read", "write", "delete")
	h.seed(t)
	session := h.connect(t)

	out := decode[deleteCountOut](t, call(t, session, "delete_quarantined", map[string]any{
		"submission_ids": []string{"spam1", "s1", "s2"},
	}))
	if out.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 — only the quarantined row should go", out.Deleted)
	}
	for _, id := range []string{"s1", "s2"} {
		if _, err := h.store.GetSubmission(id); err != nil {
			t.Errorf("accepted submission %s was deleted through the quarantine tool: %v", id, err)
		}
	}
	if !strings.Contains(out.Message, "not in quarantine") {
		t.Errorf("Message = %q; it must say the rest were not deleted rather than imply they were", out.Message)
	}
}

// TestListSubmissionsReadIsFilteredBeforePaging is the protocol-level
// regression test for the bug the code review found.
//
// The read filter used to be applied to the page the store had already chosen
// with LIMIT and OFFSET, so an inbox where every read submission sorts behind a
// full page of unread ones answered "you have no read messages". The seeded
// fixture in this file has only three submissions, so it passed either way —
// which is the point: this one is shaped to break the old code.
func TestListSubmissionsReadIsFilteredBeforePaging(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	if err := h.store.CreateForm(store.Form{ID: "contact", Name: "Contact"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	if err := h.store.CreateSubmission(store.Submission{
		ID: "old-read", FormID: "contact", RawData: `{"message":"answered last week"}`,
		CreatedAt: base.Add(-100 * time.Hour),
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if err := h.store.MarkRead("old-read"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	// A full default page of newer, unread submissions on top of it.
	for i := range 30 {
		id := fmt.Sprintf("unread-%02d", i)
		if err := h.store.CreateSubmission(store.Submission{
			ID: id, FormID: "contact", RawData: `{"message":"new"}`,
			CreatedAt: base.Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", id, err)
		}
	}

	out := decode[listSubmissionsOut](t, call(t, h.connect(t), "list_submissions",
		map[string]any{"form_id": "contact", "status": "read"}))

	if out.Count != 1 || len(out.Submissions) != 1 || out.Submissions[0].ID != "old-read" {
		t.Fatalf("status=read returned count=%d %v, want exactly old-read.\n"+
			"A read submission behind a screenful of unread ones is invisible, "+
			"which reads to a client as an empty inbox.", out.Count, out.Submissions)
	}
	if !out.Submissions[0].Read {
		t.Error("the returned submission is not marked read")
	}
}

// TestSubmitterIPsAreWithheldByDefault closes the PII follow-up.
//
// A submission's IP is the operator's own data, and it is what an IP block rule
// is written from — but an MCP client is a language model with a context window
// and, often, a vendor behind it. Shipping every submitter's address into that
// by default is a decision nobody made deliberately, so the default is now to
// withhold, and an operator turns it on if they want it.
//
// Asserted on both listings and on the single-submission read, because "we only
// leak it in one place" is the shape this kind of fix usually takes.
func TestSubmitterIPsAreWithheldByDefault(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	h.seed(t)
	session := h.connect(t)

	list := decode[listSubmissionsOut](t, call(t, session, "list_submissions", map[string]any{"status": "all"}))
	if len(list.Submissions) == 0 {
		t.Fatal("no submissions returned")
	}
	for _, sub := range list.Submissions {
		if sub.IP != "" {
			t.Errorf("list_submissions returned the submitter IP %q for %s", sub.IP, sub.ID)
		}
	}

	one := decode[getSubmissionOut](t, call(t, session, "get_submission", map[string]any{"submission_id": "s1"}))
	if one.Submission.IP != "" {
		t.Errorf("get_submission returned the submitter IP %q", one.Submission.IP)
	}

	held := decode[listQuarantineOut](t, call(t, session, "list_quarantine", nil))
	if len(held.Submissions) == 0 {
		t.Fatal("no quarantined submissions returned")
	}
	for _, sub := range held.Submissions {
		if sub.IP != "" {
			t.Errorf("list_quarantine returned the submitter IP %q for %s", sub.IP, sub.ID)
		}
	}

	search := decode[listSubmissionsOut](t, call(t, session, "search_submissions", map[string]any{"query": "pricing"}))
	for _, sub := range search.Submissions {
		if sub.IP != "" {
			t.Errorf("search_submissions returned the submitter IP %q for %s", sub.IP, sub.ID)
		}
	}
}

// TestSubmitterIPsAppearWhenTheOperatorAsks is the other half. A switch that
// only ever withheld would satisfy the test above just as well, and would make
// IP block rules unwritable from a client.
func TestSubmitterIPsAppearWhenTheOperatorAsks(t *testing.T) {
	t.Parallel()
	h := newHarnessWithIPs(t, "read")
	h.seed(t)
	session := h.connect(t)

	list := decode[listSubmissionsOut](t, call(t, session, "list_submissions", map[string]any{"status": "all"}))
	var seen bool
	for _, sub := range list.Submissions {
		if sub.IP != "" {
			seen = true
		}
	}
	if !seen {
		t.Error("no submitter IP was returned although the instance opted in")
	}

	one := decode[getSubmissionOut](t, call(t, session, "get_submission", map[string]any{"submission_id": "s1"}))
	if one.Submission.IP != "198.51.100.1" {
		t.Errorf("get_submission IP = %q, want the stored value", one.Submission.IP)
	}
}

// TestActorNamesTheTokenNotJustTheUser.
//
// Found by pointing a real MCP client at a running instance: the signal a
// mark_spam leaves read "admin", although the token was called
// "isolated-agent". Tokens are per-user, so that was not wrong — but with
// several tokens on one account it cannot say which client acted, which is
// exactly the question asked when one misbehaves.
//
// The fallbacks matter as much as the happy path: a record that cannot name who
// made it is still better than one that names nobody, so each degradation drops
// to the next most specific thing rather than to an empty string.
func TestActorNamesTheTokenNotJustTheUser(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")

	admin, err := h.store.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	srv := New(h.store, "test", Options{})

	req := func(info *auth.TokenInfo) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: info}}
	}
	withToken := func(name string) *auth.TokenInfo {
		return &auth.TokenInfo{UserID: admin.ID, Extra: map[string]any{TokenNameKey: name}}
	}

	tests := []struct {
		name string
		req  *mcp.CallToolRequest
		want string
	}{
		{"user and token", req(withToken("isolated-agent")), "admin (isolated-agent)"},
		{"a token with no name falls back to the user", req(withToken("")), "admin"},
		{"no Extra at all falls back to the user", req(&auth.TokenInfo{UserID: admin.ID}), "admin"},
		{"an unknown user falls back to the id", req(&auth.TokenInfo{UserID: "nope"}), "nope"},
		{"no user id at all", req(&auth.TokenInfo{}), "an api token"},
		{"no token info", &mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}, "an api token"},
		{"nil request", nil, "an api token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := srv.actor(tt.req); got != tt.want {
				t.Errorf("actor() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestActorIsBounded. The token name is operator-supplied and the CLI does not
// cap it, so an actor string is attacker-adjacent input on its way into a column
// the quarantine screen renders.
func TestActorIsBounded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "read")
	admin, err := h.store.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	srv := New(h.store, "test", Options{})

	got := srv.actor(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{
		UserID: admin.ID,
		Extra:  map[string]any{TokenNameKey: strings.Repeat("x", 500)},
	}}})
	if len(got) > maxActorLen {
		t.Errorf("actor() is %d characters, want at most %d", len(got), maxActorLen)
	}
	if !strings.HasPrefix(got, "admin (") {
		t.Errorf("actor() = %.40q, want it still to name the user", got)
	}
}

// TestMarkSpamRecordsTheTokenName is the same thing end to end, through the real
// protocol, landing in the column the quarantine screen reads.
func TestMarkSpamRecordsTheTokenName(t *testing.T) {
	t.Parallel()
	h := newHarnessNamed(t, "my-laptop", "read", "write")
	h.seed(t)

	if res := call(t, h.connect(t), "mark_spam", map[string]any{"submission_id": "s2"}); res.IsError {
		t.Fatalf("mark_spam: %s", resultText(res))
	}
	signals, err := h.store.SubmissionSignals("s2")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(signals))
	}
	if signals[0].Match != "admin (my-laptop)" {
		t.Errorf("Match = %q, want the user and the token that acted", signals[0].Match)
	}
}
