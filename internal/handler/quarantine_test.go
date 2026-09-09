package handler

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/filter"
	"github.com/barancezayirli/dsforms/internal/mail"
	"github.com/barancezayirli/dsforms/internal/spam"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

func setupQuarantine(t *testing.T) (*store.Store, *mail.MockMailer, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}

	base := template.Must(template.New("base").Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	page := template.Must(template.Must(base.Clone()).Parse(
		`{{define "content"}}{{range .Rows}}<span class="held">{{.ID}}</span>{{end}}` +
			`{{if .Selected}}<span class="sel">{{.Selected.ID}}</span>` +
			`<span class="meter">{{$.MeterPercent}}</span>{{end}}{{end}}` +
			`{{define "held-panel"}}<span class="panel">{{if .Selected}}{{.Selected.ID}}{{end}}</span>{{end}}`))
	rules := template.Must(template.Must(base.Clone()).Parse(
		`{{define "content"}}{{range .Block}}<span class="block">{{.Value}}</span>{{end}}` +
			`{{range .Allow}}<span class="allow">{{.Value}}</span>{{end}}` +
			`{{if .Error}}<span class="err">{{.Error}}</span>{{end}}{{end}}`))

	m := mail.NewMockMailer()
	h := &QuarantineHandler{
		Base: Base{
			Store:     s,
			SecretKey: testSecretKey,
			BaseURL:   "https://example.com",
			Templates: map[string]*template.Template{"quarantine.html": page, "rules.html": rules},
		},
		Notifier:         m,
		RetentionDays:    30,
		DefaultThreshold: spam.DefaultThreshold,
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/quarantine", h.Page)
		r.Post("/admin/quarantine/{id}/restore", h.Restore)
		r.Post("/admin/quarantine/{id}/report", h.Report)
		r.Post("/admin/quarantine/delete", h.Delete)
		r.Post("/admin/quarantine/empty", h.Empty)
		r.Get("/admin/rules", h.RulesPage)
		r.Post("/admin/rules", h.AddRule)
	})
	return s, m, r
}

func seedHeld(t *testing.T, s *store.Store, id string, score int, signals []store.SpamSignal) {
	t.Helper()
	sub := store.Submission{
		ID: id, FormID: "f1",
		Data:      map[string]string{"name": "Bot", "email": "bot@spam.example"},
		RawData:   `{"name":"Bot","email":"bot@spam.example"}`,
		IP:        "203.0.113.5",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateHeldSubmission(sub, score, spam.DefaultThreshold, signals); err != nil {
		t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
	}
}

func TestQuarantinePageListsHeld(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, []store.SpamSignal{{Rule: "markup", Field: "message", Match: "[url=", Weight: 6}})
	seedHeld(t, s, "h2", 12, nil)

	w := doAdminRequest(t, s, r, "GET", "/admin/quarantine", "")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	for _, id := range []string{"h1", "h2"} {
		if !strings.Contains(body, id) {
			t.Errorf("%s not listed", id)
		}
	}
	// The panel defaults to a selection rather than sitting empty beside a full queue.
	if !strings.Contains(body, `class="sel"`) {
		t.Error("no submission selected by default")
	}
}

// The meter's scale is max(12, score), so a submission that scraped in at the
// threshold does not look identical to one that tripled it.
func TestQuarantineMeterScale(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, nil)

	body := doAdminRequest(t, s, r, "GET", "/admin/quarantine?sel=h1", "").Body.String()
	if !strings.Contains(body, `<span class="meter">50</span>`) {
		t.Errorf("meter should be 50%% for a score of 6 against a scale of 12; got %s", body)
	}
}

func TestQuarantinePanelFragment(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, nil)
	seedHeld(t, s, "h2", 9, nil)

	req := httptest.NewRequest("GET", "/admin/quarantine?sel=h2", nil)
	req.Header.Set("X-Fragment", "1")
	req.AddCookie(loginCookie(t, s))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `class="panel"`) {
		t.Fatalf("fragment did not render the panel: %q", body)
	}
	if strings.Contains(body, `class="held"`) {
		t.Error("fragment rendered the whole page, not just the panel")
	}
	if !strings.Contains(body, "h2") {
		t.Error("fragment did not honour the requested selection")
	}
}

// Restoring is the feature's reason to exist: the submission returns to its
// form unread, and the notification withheld while it sat in quarantine is
// finally sent.
func TestQuarantineRestoreSendsWithheldNotification(t *testing.T) {
	t.Parallel()
	s, m, r := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, []store.SpamSignal{{Rule: "markup", Field: "message", Match: "[url=", Weight: 6}})

	w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "h1" {
		t.Fatalf("restored submission is not in the inbox: %v", subs)
	}
	if subs[0].Read {
		t.Error("a restored submission must be unread — nobody has seen it")
	}

	if !m.Wait(2 * time.Second) {
		t.Fatal("the notification withheld during quarantine was never sent")
	}

	// The breakdown survives: it is the evidence of a false positive.
	signals, err := s.SubmissionSignals("h1")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(signals) != 1 {
		t.Errorf("signals discarded on restore: got %d, want 1", len(signals))
	}
}

func TestQuarantineDeleteAndEmpty(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	for _, id := range []string{"h1", "h2", "h3"} {
		seedHeld(t, s, id, 6, nil)
	}

	form := url.Values{"ids": {"h1", "h2"}}
	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/delete", form.Encode()); w.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", w.Code)
	}
	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 1 || held[0].ID != "h3" {
		t.Fatalf("after bulk delete: %v, want only h3", held)
	}

	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/empty", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("empty status = %d, want 303", w.Code)
	}
	held, _ = s.HeldSubmissions(10, 0)
	if len(held) != 0 {
		t.Errorf("quarantine not emptied: %v", held)
	}
}

// A false-positive report has nowhere to go on a self-hosted instance, so it is
// logged rather than discarded. What matters here is that it does not 500 and
// does not delete anything.
func TestQuarantineReportKeepsTheSubmission(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, nil)

	w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/report", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	held, _ := s.HeldSubmissions(10, 0)
	if len(held) != 1 {
		t.Error("reporting a false positive must not remove the submission")
	}
}

func TestQuarantineReportUnknownID(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/nope/report", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestRulesPageAddAndValidate(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)

	form := url.Values{"kind": {"block"}, "type": {"domain"}, "value": {"Spam.Example"}}
	if w := doAdminRequest(t, s, r, "POST", "/admin/rules", form.Encode()); w.Code != http.StatusSeeOther {
		t.Fatalf("add status = %d, want 303", w.Code)
	}

	body := doAdminRequest(t, s, r, "GET", "/admin/rules", "").Body.String()
	if !strings.Contains(body, "spam.example") {
		t.Errorf("rule not listed normalised: %q", body)
	}

	// A bad value is the operator mistyping: it re-renders with an inline error
	// rather than 500ing or silently succeeding.
	bad := url.Values{"kind": {"block"}, "type": {"ip"}, "value": {"not-an-ip"}}
	w := doAdminRequest(t, s, r, "POST", "/admin/rules", bad.Encode())
	if w.Code != http.StatusOK {
		t.Fatalf("invalid rule status = %d, want 200 (re-render)", w.Code)
	}
	if !strings.Contains(w.Body.String(), `class="err"`) {
		t.Error("no inline error shown for an invalid rule")
	}
	rules, _ := s.ListFilterRules()
	if len(rules) != 1 {
		t.Errorf("invalid rule was persisted: %v", rules)
	}
}

func TestRulesSplitByKind(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeDomain, "spam.example", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	if _, err := s.AddFilterRule(filter.KindAllow, filter.TypeEmail, "vip@example.com", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	body := doAdminRequest(t, s, r, "GET", "/admin/rules", "").Body.String()
	if !strings.Contains(body, `<span class="block">spam.example</span>`) {
		t.Error("block rule not in the blocklist")
	}
	if !strings.Contains(body, `<span class="allow">vip@example.com</span>`) {
		t.Error("allow rule not in the allowlist")
	}
}
