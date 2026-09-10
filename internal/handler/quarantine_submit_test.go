package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/filter"
	"github.com/barancezayirli/dsforms/internal/mail"
	"github.com/barancezayirli/dsforms/internal/spam"
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
		Tracker:          spam.NewTracker(1000),
		DefaultThreshold: spam.DefaultThreshold,
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
	if held[0].SpamScore < spam.DefaultThreshold {
		t.Errorf("SpamScore = %d, want >= %d", held[0].SpamScore, spam.DefaultThreshold)
	}
	if held[0].HeldThreshold != spam.DefaultThreshold {
		t.Errorf("HeldThreshold = %d, want %d", held[0].HeldThreshold, spam.DefaultThreshold)
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

	if _, err := s.AddFilterRule(filter.KindAllow, filter.TypeEmail, "vip@example.com", ""); err != nil {
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

	if _, err := s.AddFilterRule(filter.KindAllow, filter.TypeEmail, "vip@example.com", "known good"); err != nil {
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

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeDomain, "spam.example", ""); err != nil {
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
	if len(signals) != 1 || signals[0].Rule != spam.RuleBlocked {
		t.Fatalf("signals = %+v, want a single 'rule' signal naming the blocklist entry", signals)
	}
	// The weights-sum-to-score invariant applies here too. The scored path
	// asserts it; the two handler-stamped paths are where it is easiest to break,
	// because the score and the signal are set by separate statements.
	if signals[0].Weight != held[0].SpamScore {
		t.Errorf("signal weight %d does not match the stored score %d", signals[0].Weight, held[0].SpamScore)
	}
	if held[0].SpamScore != spam.DefaultThreshold {
		t.Errorf("SpamScore = %d, want the threshold %d — a zero here renders the meter empty",
			held[0].SpamScore, spam.DefaultThreshold)
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
		if sig.Rule == "repeat_ip" {
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

	if _, err := s.AddFilterRule(filter.KindAllow, filter.TypeEmail, "vip@example.com", "office"); err != nil {
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

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeCIDR, "203.0.113.0/24", ""); err != nil {
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

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeKeyword, "telegram pump", ""); err != nil {
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
