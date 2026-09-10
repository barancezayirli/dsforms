package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/mail"
	"github.com/barancezayirli/dsforms/internal/screen"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

// submitTo posts a form body to the handler and returns the recorder.
func submitTo(t *testing.T, h *SubmitHandler, formID string, fields map[string]string, ip string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodPost, "/f/"+formID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	rr := httptest.NewRecorder()

	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)
	r.ServeHTTP(rr, req)
	return rr
}

func quarantineHandler(t *testing.T) (*SubmitHandler, *store.Store, *mail.MockMailer) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	m := mail.NewMockMailer()
	h := &SubmitHandler{
		Store:            s,
		Notifier:         m,
		BaseURL:          "https://example.com",
		Screener:         screen.New(1000),
		DefaultThreshold: screen.DefaultThreshold,
	}
	return h, s, m
}

// The headline behaviour change: spam is held for review instead of vanishing.
func TestSubmitHoldsSpamInsteadOfDropping(t *testing.T) {
	t.Parallel()
	h, s, m := quarantineHandler(t)

	rr := submitTo(t, h, "f1", map[string]string{
		"name": "Bot", "message": "[url=http://x.example]buy[/url]",
	}, "203.0.113.5")

	// The response is indistinguishable from success, as before — a bot must not
	// learn that it was caught.
	if rr.Code != http.StatusFound && rr.Code != http.StatusOK {
		t.Errorf("status = %d, want a success response", rr.Code)
	}

	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("got %d held submissions, want 1 — spam should be quarantined, not dropped", len(held))
	}
	if held[0].SpamScore < screen.DefaultThreshold {
		t.Errorf("SpamScore = %d, want >= %d", held[0].SpamScore, screen.DefaultThreshold)
	}
	if held[0].HeldThreshold != screen.DefaultThreshold {
		t.Errorf("HeldThreshold = %d, want %d", held[0].HeldThreshold, screen.DefaultThreshold)
	}

	// The breakdown must be stored, or the quarantine screen has nothing to show.
	signals, err := s.SubmissionSignals(held[0].ID)
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(signals) == 0 {
		t.Error("no signals stored for a held submission")
	}
	sum := 0
	for _, sig := range signals {
		sum += sig.Weight
	}
	if sum != held[0].SpamScore {
		t.Errorf("stored signals sum to %d but spam_score is %d", sum, held[0].SpamScore)
	}

	// And crucially: no notification for something nobody has reviewed. Wait
	// briefly first — a notification would be sent from a goroutine, so an
	// immediate count could pass simply by racing it.
	if m.Wait(200 * time.Millisecond) {
		t.Errorf("a held submission sent a notification; want none")
	}
	// It must also stay out of the form's inbox.
	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("held submission leaked into the inbox: %v", subs)
	}
}

func TestSubmitCleanSubmissionStillNotifies(t *testing.T) {
	t.Parallel()
	h, s, m := quarantineHandler(t)

	submitTo(t, h, "f1", map[string]string{
		"name": "Jane Doe", "email": "jane@example.com", "message": "Hello, I loved your work.",
	}, "203.0.113.6")

	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("got %d submissions, want 1", len(subs))
	}
	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 0 {
		t.Errorf("a clean submission was held: %v", held)
	}
	if !m.Wait(2 * time.Second) {
		t.Fatal("no notification sent for a clean submission")
	}
	if n := m.CallCount(); n != 1 {
		t.Errorf("notifications sent = %d, want 1", n)
	}
}

// The honeypot runs before the allow list. Reversing them, as the design handoff
// specified, would let a bot bypass the honeypot entirely by putting an
// allowlisted address in the email field — which is attacker-controlled.
func TestSubmitHoneypotBeatsAllowRule(t *testing.T) {
	t.Parallel()
	h, s, _ := quarantineHandler(t)

	if _, err := s.AddFilterRule(screen.KindAllow, screen.TypeEmail, "vip@example.com", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	submitTo(t, h, "f1", map[string]string{
		"email": "vip@example.com", "message": "hi", "_honeypot": "gotcha",
	}, "203.0.113.7")

	subs, _ := s.ListSubmissions("f1")
	held, _ := s.HeldSubmissions(10, 0)
	if len(subs) != 0 || len(held) != 0 {
		t.Errorf("honeypot-filled submission was stored (subs=%d held=%d) — the allow list "+
			"must not be able to bypass the honeypot", len(subs), len(held))
	}
}

// An allowlisted sender skips scoring entirely, so content that would otherwise
// be held goes straight through.
func TestSubmitAllowRuleSkipsScoring(t *testing.T) {
	t.Parallel()
	h, s, m := quarantineHandler(t)

	if _, err := s.AddFilterRule(screen.KindAllow, screen.TypeEmail, "vip@example.com", "known good"); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	submitTo(t, h, "f1", map[string]string{
		"email": "vip@example.com", "message": "[url=http://x.example]see my site[/url]",
	}, "203.0.113.8")

	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 0 {
		t.Fatalf("an allowlisted sender was held anyway: %v", held)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 1 {
		t.Fatalf("got %d submissions, want 1", len(subs))
	}
	if !m.Wait(2 * time.Second) {
		t.Fatal("no notification sent for an allowlisted submission")
	}

	rules, _ := s.ListFilterRules()
	if len(rules) != 1 || rules[0].Hits != 1 {
		t.Errorf("allow rule hit count = %v, want 1", rules)
	}
}

// A block rule holds on arrival whatever the content scores.
func TestSubmitBlockRuleHoldsImmediately(t *testing.T) {
	t.Parallel()
	h, s, m := quarantineHandler(t)

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "spam.example", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	submitTo(t, h, "f1", map[string]string{
		"email": "someone@spam.example", "message": "a perfectly ordinary message",
	}, "203.0.113.9")

	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("got %d held, want 1 — a block rule should hold regardless of score", len(held))
	}
	if m.Wait(200 * time.Millisecond) {
		t.Error("a blocked submission sent a notification; want none")
	}

	signals, _ := s.SubmissionSignals(held[0].ID)
	if len(signals) != 1 || signals[0].Check != screen.CheckBlocked {
		t.Fatalf("signals = %+v, want a single 'rule' signal naming the blocklist entry", signals)
	}
	// The weights-sum-to-score invariant applies here too. The scored path
	// asserts it; the two handler-stamped paths are where it is easiest to break,
	// because the score and the signal are set by separate statements.
	if signals[0].Weight != held[0].SpamScore {
		t.Errorf("signal weight %d does not match the stored score %d", signals[0].Weight, held[0].SpamScore)
	}
	if held[0].SpamScore != screen.DefaultThreshold {
		t.Errorf("SpamScore = %d, want the threshold %d — a zero here renders the meter empty",
			held[0].SpamScore, screen.DefaultThreshold)
	}
	if signals[0].Match != "spam.example" {
		t.Errorf("signal Match = %q, want the rule value", signals[0].Match)
	}

	rules, _ := s.ListFilterRules()
	if len(rules) != 1 || rules[0].Hits != 1 {
		t.Errorf("block rule hit count = %v, want 1", rules)
	}
}

// Repeat-IP abuse used to be a silent drop too. It must now be reviewable, and
// must say so in the breakdown rather than looking like a content match.
func TestSubmitRepeatIPIsHeldWithItsOwnSignal(t *testing.T) {
	t.Parallel()
	h, s, _ := quarantineHandler(t)

	const ip = "203.0.113.10"
	// The tracker holds from the third submission onward.
	for i := 0; i < 3; i++ {
		submitTo(t, h, "f1", map[string]string{"message": "hello there, a normal message"}, ip)
	}

	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) == 0 {
		t.Fatal("repeated submissions from one IP were dropped rather than held")
	}

	signals, _ := s.SubmissionSignals(held[0].ID)
	var found bool
	for _, sig := range signals {
		if sig.Check == "repeat_ip" {
			found = true
			if sig.Match != ip {
				t.Errorf("repeat_ip signal Match = %q, want the IP %q", sig.Match, ip)
			}
		}
	}
	if !found {
		t.Errorf("held submission has no repeat_ip signal: %+v", signals)
	}

	sum := 0
	for _, sig := range signals {
		sum += sig.Weight
	}
	if sum != held[0].SpamScore {
		t.Errorf("signals sum to %d but the stored score is %d — the breakdown would not add up",
			sum, held[0].SpamScore)
	}
}

// An allowlisted sender must skip the repeat-IP check too. The allow branch sits
// before the scoring branch precisely so a busy office NAT is not held, and that
// ordering is invisible to a test that submits once from one IP.
func TestSubmitAllowRuleSkipsRepeatIP(t *testing.T) {
	t.Parallel()
	h, s, _ := quarantineHandler(t)

	if _, err := s.AddFilterRule(screen.KindAllow, screen.TypeEmail, "vip@example.com", "office"); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	const ip = "203.0.113.50"
	for i := 0; i < 4; i++ {
		submitTo(t, h, "f1", map[string]string{
			"email": "vip@example.com", "message": "a normal enquiry",
		}, ip)
	}

	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("an allowlisted sender was held for repeat IP: %v", held)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 4 {
		t.Errorf("accepted %d of 4 submissions from an allowlisted sender", len(subs))
	}
}

// The signal for a blocked submission must not put a rule *type* in Field —
// Field means "the form field whose value matched", and the quarantine panel
// renders it as "field <x> · matched", so a CIDR rule read "field cidr".
func TestSubmitBlockRuleLeavesFieldEmpty(t *testing.T) {
	t.Parallel()
	h, s, _ := quarantineHandler(t)

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeCIDR, "203.0.113.0/24", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	submitTo(t, h, "f1", map[string]string{"message": "ordinary"}, "203.0.113.9")

	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 1 {
		t.Fatalf("got %d held, want 1", len(held))
	}
	signals, _ := s.SubmissionSignals(held[0].ID)
	if len(signals) != 1 {
		t.Fatalf("signals = %+v, want 1", signals)
	}
	if signals[0].Field != "" {
		t.Errorf("Field = %q, want empty — it names a form field, not a rule type", signals[0].Field)
	}
	if signals[0].Match != "203.0.113.0/24" {
		t.Errorf("Match = %q, want the rule value", signals[0].Match)
	}
}

// The per-form override beats the instance default in both directions.
func TestSubmitPerFormThresholdOverride(t *testing.T) {
	t.Parallel()

	t.Run("strict form holds what the default would accept", func(t *testing.T) {
		t.Parallel()
		h, s, _ := quarantineHandler(t)
		f, _ := s.GetForm("f1")
		f.SpamThreshold = 4 // Strict: a lone keyword (+5) now holds.
		if err := s.UpdateForm(f); err != nil {
			t.Fatalf("UpdateForm: %v", err)
		}

		submitTo(t, h, "f1", map[string]string{"message": "buy backlinks now"}, "203.0.113.11")

		held, _ := s.HeldSubmissions(10, 0)
		if len(held) != 1 {
			t.Fatalf("got %d held at threshold 4, want 1 — a keyword scores 5", len(held))
		}
		if held[0].HeldThreshold != 4 {
			t.Errorf("HeldThreshold = %d, want the 4 that was actually applied", held[0].HeldThreshold)
		}
	})

	t.Run("lenient form accepts what the default would hold", func(t *testing.T) {
		t.Parallel()
		h, s, _ := quarantineHandler(t)
		f, _ := s.GetForm("f1")
		f.SpamThreshold = 9 // Lenient: a lone markup link (+6) no longer holds.
		if err := s.UpdateForm(f); err != nil {
			t.Fatalf("UpdateForm: %v", err)
		}

		submitTo(t, h, "f1", map[string]string{"message": "[url=http://x.example]hi[/url]"}, "203.0.113.12")

		held, _ := s.HeldSubmissions(10, 0)
		if len(held) != 0 {
			t.Errorf("got %d held at threshold 9, want 0 — markup scores only 6", len(held))
		}
		subs, _ := s.ListSubmissions("f1")
		if len(subs) != 1 {
			t.Errorf("got %d accepted, want 1", len(subs))
		}
	})
}

// Custom keywords extend the built-in list at the usual weight, so one of them
// still cannot hold a submission on its own.
func TestSubmitCustomKeywordPilesUpButDoesNotHoldAlone(t *testing.T) {
	t.Parallel()
	h, s, _ := quarantineHandler(t)

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeKeyword, "telegram pump", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	submitTo(t, h, "f1", map[string]string{"message": "join our telegram pump group"}, "203.0.113.13")
	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 0 {
		t.Errorf("a single custom keyword held a submission on its own: %v", held)
	}

	// Same keyword alongside a second signal does hold.
	submitTo(t, h, "f1", map[string]string{
		"message": "join our telegram pump group [url=http://x.example]here[/url]",
	}, "203.0.113.14")
	held, _ = s.HeldSubmissions(10, 0)
	if len(held) != 1 {
		t.Errorf("got %d held, want 1 once the keyword piles up with markup", len(held))
	}
}

// TestAcceptedSubmissionsStoreTheirVerdict covers a number the UI was inventing.
//
// Score, threshold and signals are computed for every submission and were
// persisted only on the held path, so the reader rendered the column default and
// every accepted submission claimed "score 0". A message sent during a feature
// pass really scored 3 — a gibberish token on the ordinary English word
// "months" — and the drawer still said 0.
//
// Each row asserts the stored values equal what the screener actually returned,
// rather than hardcoding a number. Writing `SpamScore == 3` pins the scorer's
// current tuning as well as the persistence contract: retune gibberishWeight and
// this test fails saying "stored SpamScore = 4, want 3", in the handler package,
// pointing a reader at submit.go and store.go — both of which would be correct.
// Comparing against the verdict pins the thing this test is about.
func TestAcceptedSubmissionsStoreTheirVerdict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// formThreshold is the form's own override; 0 means inherit.
		formThreshold int
		fields        map[string]string
		wantScoreZero bool
	}{
		{
			name: "scores something and is still delivered",
			fields: map[string]string{
				"name":    "Ana Silva",
				"email":   "ana@example.org",
				"message": "We are planning a crypto rollout next quarter and wanted your thoughts.",
			},
		},
		{
			name: "genuinely clean",
			fields: map[string]string{
				"name":    "Ben Cole",
				"email":   "ben@example.org",
				"message": "We are planning a rebrand next quarter and wanted your thoughts.",
			},
			wantScoreZero: true,
		},
		{
			// The row that makes the threshold's provenance testable. Without a
			// form-level override, effectiveThreshold and the verdict's threshold
			// are the same number, so storing either one passes — and the comment
			// in submit.go explaining why the verdict's clamped value is used
			// would be the only guard on it. 99 is above MaxThreshold, so the two
			// differ: the screener clamps to 20 and judged the submission against
			// that, not against 99.
			name:          "a form threshold the screener clamped",
			formThreshold: 99,
			fields: map[string]string{
				"name":    "Cara Lin",
				"email":   "cara@example.org",
				"message": "We are planning a crypto rollout next quarter and wanted your thoughts.",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, s, _ := quarantineHandler(t)
			if tc.formThreshold > 0 {
				if err := s.UpdateForm(store.Form{
					ID: "f1", Name: "Contact", EmailTo: "me@example.com",
					SpamThreshold: tc.formThreshold,
				}); err != nil {
					t.Fatalf("UpdateForm: %v", err)
				}
			}

			rr := submitTo(t, h, "f1", tc.fields, "203.0.113.10")
			if rr.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302 — every row here is below its threshold", rr.Code)
			}

			// What the screener actually decided, for the same input.
			form, err := s.GetForm("f1")
			if err != nil {
				t.Fatalf("GetForm: %v", err)
			}
			want := h.Screener.Decide(screen.Input{
				FormID: "f1", Fields: tc.fields, IP: "203.0.113.10",
				Threshold: h.effectiveThreshold(form),
			})

			subs, err := s.ListSubmissions("f1")
			if err != nil {
				t.Fatalf("ListSubmissions: %v", err)
			}
			if len(subs) != 1 {
				t.Fatalf("stored %d submissions, want 1 (accepted, not held)", len(subs))
			}
			got := subs[0]

			if got.IsHeld {
				t.Fatal("submission was held; these rows are about the accepted path")
			}
			if got.SpamScore != want.Score {
				t.Errorf("stored SpamScore = %d, want %d — the score the screener returned.\n"+
					"The drawer renders this field, so a wrong value here is the UI "+
					"stating a score the submission never had.", got.SpamScore, want.Score)
			}
			if (got.SpamScore == 0) != tc.wantScoreZero {
				t.Errorf("SpamScore = %d, but this row expects zero=%v — the fixture no "+
					"longer exercises the case it was written for", got.SpamScore, tc.wantScoreZero)
			}
			if got.HeldThreshold != want.Threshold {
				t.Errorf("stored HeldThreshold = %d, want %d — the bar actually applied.\n"+
					"Decide clamps, so persisting the form's raw setting would record a "+
					"threshold the submission was never judged against.",
					got.HeldThreshold, want.Threshold)
			}

			// The decision this branch's comment defends: an accepted submission
			// stores no signal rows. The reader tells "was held, then restored"
			// from "passed" by whether any exist, so writing them here would make
			// ordinary submissions claim they had been quarantined.
			signals, err := s.SubmissionSignals(got.ID)
			if err != nil {
				t.Fatalf("SubmissionSignals: %v", err)
			}
			if len(signals) != 0 {
				t.Errorf("stored %d signal rows for an accepted submission, want 0.\n"+
					"The detail page branches on their presence, so this would make it "+
					"report that a delivered submission had been held and restored.",
					len(signals))
			}
		})
	}
}
