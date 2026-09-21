package handler

import (
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/flash"
	"github.com/barancezayirli/dsforms/internal/mail"
	"github.com/barancezayirli/dsforms/internal/screen"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

func setupQuarantine(t *testing.T) (*store.Store, *mail.MockMailer, *chi.Mux) {
	s, m, _, r := setupQuarantineWithWebhook(t)
	return s, m, r
}

// setupQuarantineWithWebhook also exposes the webhook recorder, for the tests
// that care that a restore makes good on *both* withheld notifications.
func setupQuarantineWithWebhook(t *testing.T) (*store.Store, *mail.MockMailer, *mockWebhookSender, *chi.Mux) {
	t.Helper()
	m := mail.NewMockMailer()
	s, wh, r := setupQuarantineWithMailer(t, m)
	return s, m, wh, r
}

// setupQuarantineWithFailingMailer wires a mailer whose sends all fail, for the
// assertions about what still has to happen when the email does not.
func setupQuarantineWithFailingMailer(t *testing.T) (*store.Store, *chi.Mux, *mockWebhookSender) {
	t.Helper()
	s, wh, r := setupQuarantineWithMailer(t, mail.NewFailingMockMailer(errors.New("smtp: connection refused")))
	return s, r, wh
}

func setupQuarantineWithMailer(t *testing.T, m *mail.MockMailer) (*store.Store, *mockWebhookSender, *chi.Mux) {
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
	// The stub mirrors the real rules.html closely enough to assert on: the
	// problems block is what these tests check, and a stub that omitted it would
	// make a passing test meaningless.
	rules := template.Must(template.Must(base.Clone()).Parse(
		`{{define "content"}}{{range .Block}}<span class="block">{{.Value}}</span>{{end}}` +
			`{{range .Allow}}<span class="allow">{{.Value}}</span>{{end}}` +
			`{{if .Problems}}<div class="problems">{{len .Problems}} rule(s) can never match:` +
			`{{range .Problems}}<span class="problem">{{.Value}} — {{.Reason}}</span>{{end}}</div>{{end}}` +
			`{{if .Error}}<span class="err">{{.Error}}</span>{{end}}{{end}}`))

	wh := newMockWebhookSender()
	h := &QuarantineHandler{
		Store: s,
		Base: Base{
			Nav:       s,
			SecretKey: testSecretKey,
			BaseURL:   "https://example.com",
			Templates: map[string]*template.Template{"quarantine.html": page, "rules.html": rules},
		},
		Notifier:         m,
		Webhook:          wh,
		RetentionDays:    30,
		DefaultThreshold: screen.DefaultThreshold,
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
		r.Post("/admin/rules/{id}/delete", h.DeleteRule)
	})
	return s, wh, r
}

// flashFrom decodes the flash a response set, so a test can assert what the
// operator was actually told rather than only which status code came back. A
// redirect carries no body, so the message is the only user-visible output of
// most mutations here.
func flashFrom(t *testing.T, w *httptest.ResponseRecorder) (msgType, message string) {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name != flash.CookieName {
			continue
		}
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(c)
		return flash.Get(req, httptest.NewRecorder(), testSecretKey)
	}
	return "", ""
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
	if err := s.CreateHeldSubmission(sub, score, screen.DefaultThreshold, signals); err != nil {
		t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
	}
}

func TestQuarantinePageListsHeld(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, []store.SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}})
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
	seedHeld(t, s, "h1", 6, []store.SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}})

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
	held, _ := s.HeldSubmissions(store.AllForms(), 10, 0)
	if len(held) != 1 || held[0].ID != "h3" {
		t.Fatalf("after bulk delete: %v, want only h3", held)
	}

	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/empty", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("empty status = %d, want 303", w.Code)
	}
	held, _ = s.HeldSubmissions(store.AllForms(), 10, 0)
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
	held, _ := s.HeldSubmissions(store.AllForms(), 10, 0)
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
	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "spam.example", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	if _, err := s.AddFilterRule(screen.KindAllow, screen.TypeEmail, "vip@example.com", ""); err != nil {
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

// A held submission must not be readable through the ordinary submission
// reader. That route auto-marks read, so opening one there would quietly change
// quarantine state from a screen that shows none of the review controls — and
// the URL is guessable from any held id.
func TestSubmissionReaderRejectsHeldSubmissions(t *testing.T) {
	t.Parallel()
	s, _, _ := setupQuarantine(t)
	seedHeld(t, s, "h1", 6, nil)

	base := template.Must(template.New("base").Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	page := template.Must(template.Must(base.Clone()).Parse(
		`{{define "content"}}<span class="read">{{.Submission.ID}}</span>{{end}}` +
			`{{define "drawer"}}<span class="drawer">{{.Submission.ID}}</span>{{end}}`))

	ah := &AdminHandler{
		Store: s,
		Base: Base{
			Nav: s, SecretKey: testSecretKey, BaseURL: "https://example.com",
			Templates: map[string]*template.Template{"submission_detail.html": page},
		}}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/forms/{formID}/submissions/{subID}", ah.SubmissionDetail)
	})

	w := doAdminRequest(t, s, r, "GET", "/admin/forms/f1/submissions/h1", "")
	// Pinned to the exact status rather than "not 200": a nil-map panic, a
	// template parse failure or a mis-wired route all produce a non-200 too, so
	// the loose assertion would let a crash masquerade as the guard working.
	//
	// 303 to the quarantine screen, not 404 — the submission exists and the
	// operator should land where the breakdown and the restore control are.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 to the quarantine screen", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/quarantine?sel=h1" {
		t.Errorf("Location = %q, want the quarantine screen with h1 selected", loc)
	}

	// And it must not have been marked read as a side effect.
	held, err := s.HeldSubmissions(store.AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("submission left quarantine: %v", held)
	}
	if held[0].Read {
		t.Error("the reader marked a held submission read")
	}
}

// A restore must make good on BOTH withheld notifications. The hold path
// withholds the email and the webhook; sending only the email leaves a restored
// lead sitting in the inbox and never reaching the CRM, with nothing anywhere
// to indicate the webhook was skipped.
func TestQuarantineRestoreAlsoFiresTheWebhook(t *testing.T) {
	t.Parallel()
	s, m, wh, r := setupQuarantineWithWebhook(t)

	// The seeded form needs a webhook configured for one to be owed.
	f, err := s.GetForm("f1")
	if err != nil {
		t.Fatalf("GetForm: %v", err)
	}
	f.WebhookURL = "https://hooks.example.com/abc"
	f.WebhookFormat = "generic"
	if err := s.UpdateForm(f); err != nil {
		t.Fatalf("UpdateForm: %v", err)
	}
	seedHeld(t, s, "h1", 6, nil)

	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if !m.Wait(2 * time.Second) {
		t.Error("the withheld email was not sent")
	}
	if !wh.wait(2 * time.Second) {
		t.Fatal("the withheld webhook was not sent")
	}

	// Asserting only that a webhook fired would pass if the wrong form's hook
	// fired, or if the payload carried the wrong submission.
	call, ok := wh.lastCall()
	if !ok {
		t.Fatal("no webhook call recorded")
	}
	if call.Form.ID != "f1" {
		t.Errorf("webhook form = %q, want f1", call.Form.ID)
	}
	if call.Sub.ID != "h1" {
		t.Errorf("webhook submission = %q, want the restored h1", call.Sub.ID)
	}
	if call.Sub.IsHeld {
		t.Error("webhook carried a submission still marked held")
	}
}

// A restore owes both deliveries the hold withheld. It used to owe them
// sequentially: the notifier's error returned out of the goroutine before the
// webhook block, so a dead SMTP server silently cost the operator the CRM
// delivery too — while the flash still read "Restored to Contact as unread."
//
// submit.go, the path this was modelled on, attempts both independently. The two
// copies had drifted, and the existing webhook test could not see it because
// MockMailer's sends always succeeded.
func TestQuarantineRestoreFiresTheWebhookEvenWhenEmailFails(t *testing.T) {
	t.Parallel()
	s, r, wh := setupQuarantineWithFailingMailer(t)

	f, err := s.GetForm("f1")
	if err != nil {
		t.Fatalf("GetForm: %v", err)
	}
	f.WebhookURL = "https://hooks.example.com/abc"
	f.WebhookFormat = "generic"
	if err := s.UpdateForm(f); err != nil {
		t.Fatalf("UpdateForm: %v", err)
	}
	seedHeld(t, s, "h1", 6, nil)

	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if !wh.wait(2 * time.Second) {
		t.Fatal("email failed and the webhook was never attempted")
	}

	// The email genuinely did not go, so notified must stay 0 — the flag records
	// delivery, and a restore that could not notify has not notified.
	sub, err := s.GetSubmission("h1")
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.Notified {
		t.Error("notified = true after the notification failed")
	}
	if sub.IsHeld {
		t.Error("the submission is still held; the restore itself must succeed")
	}
}

// If the form cannot be read, the restore must not happen at all — the previous
// order restored the row, skipped the notification, and flashed a success
// message with an empty form name.
func TestQuarantineRestoreAbortsWhenTheFormIsUnreadable(t *testing.T) {
	t.Parallel()
	s, m, _, r := setupQuarantineWithWebhook(t)
	seedHeld(t, s, "h1", 6, nil)

	// Delete the form out from under the held row. Foreign keys cascade, so
	// reach past the store to leave an orphan — the state a concurrent delete
	// would produce.
	if _, err := s.DB().Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if _, err := s.DB().Exec("DELETE FROM forms WHERE id = 'f1'"); err != nil {
		t.Fatalf("orphan the submission: %v", err)
	}

	w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	// The submission must still be held: a restore that could not notify is a
	// restore that should not have happened.
	held, err := s.HeldSubmissions(store.AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Errorf("submission left quarantine despite the form being unreadable: %v", held)
	}
	if m.Wait(200 * time.Millisecond) {
		t.Error("a notification was sent for a restore that did not complete")
	}
}

// Restoring twice must not send the withheld notification twice — a double
// click or a resubmitted POST is the ordinary way this happens.
func TestQuarantineRestoreIsIdempotent(t *testing.T) {
	t.Parallel()
	s, m, _, r := setupQuarantineWithWebhook(t)
	seedHeld(t, s, "h1", 6, nil)

	if w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("first restore: status = %d", w.Code)
	}
	if !m.Wait(2 * time.Second) {
		t.Fatal("first restore sent no notification")
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("second restore: status = %d", w.Code)
	}

	// No Wait here. Waiting on a timeout to prove an *absence* is a sleep in
	// disguise: it passes for the wrong reason under load, and it cannot fail
	// fast. The second restore returns before it can spawn a goroutine, so the
	// count is settled by the time the response is written.
	if n := m.CallCount(); n != 1 {
		t.Errorf("notifications sent = %d, want exactly 1", n)
	}

	// The message the operator sees has to be true. Reporting failure for a
	// submission that *is* restored sends them back to the queue to look for
	// something now sitting in the inbox — the original scar, one layer up.
	typ, msg := flashFrom(t, w)
	if typ == "error" {
		t.Errorf("second restore flashed an error (%q); the submission was already restored", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "already restored") {
		t.Errorf("flash = %q, want it to say the submission was already restored", msg)
	}
}

// DeleteRule is a security-relevant mutation — it removes a block rule — and had
// no test, while its inverse AddRule was well covered. A rule that silently
// fails to delete, or deletes the wrong one, changes what gets through the
// filter.
func TestDeleteRuleRemovesOnlyTheNamedRule(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)

	keep, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "spam.example", "")
	if err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	drop, err := s.AddFilterRule(screen.KindBlock, screen.TypeEmail, "bot@spam.example", "")
	if err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	w := doAdminRequest(t, s, r, "POST", "/admin/rules/"+drop.ID+"/delete", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if typ, msg := flashFrom(t, w); typ != "success" {
		t.Errorf("flash = (%q, %q), want a success", typ, msg)
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules remaining = %d, want 1", len(rules))
	}
	if rules[0].ID != keep.ID {
		t.Errorf("the wrong rule was deleted: %q remains, expected %q", rules[0].ID, keep.ID)
	}
}

// An unknown id must not report success. Deleting a rule the operator believes
// is gone, when it is not, is the direction that lets spam through.
func TestDeleteRuleUnknownIDDoesNotClaimSuccess(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "spam.example", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	w := doAdminRequest(t, s, r, "POST", "/admin/rules/no-such-rule/delete", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	// The point of the test, and what it did not previously check: the flash.
	// It passed against a handler that said "Rule removed." for a no-op, which
	// is the whole failure it is named for.
	typ, msg := flashFrom(t, w)
	if typ == "success" {
		t.Errorf("flashed success (%q) for a rule that does not exist", msg)
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 1 {
		t.Errorf("rules remaining = %d, want the real rule untouched", len(rules))
	}
}

// TestQuarantineRestoreDistinguishesGoneFromAlreadyRestored is the regression
// test for a fix that manufactured the opposite lie.
//
// Round 2 fixed a false negative — "could not be restored" for a submission that
// *was* restored — by treating store.ErrNotFound as "already restored". But
// GetHeldSubmission returns ErrNoRows for two different facts: the row exists and
// is no longer held, and there is no such row at all. Collapsing both into a
// success flash means an operator clicking Restore on a row the 30-day retention
// sweep deleted is told it is in their inbox. It is not, and never will be.
//
// The asymmetry is the lesson: fixing one direction of an error message without
// asking what the opposite case now reports just moves the untruth.
func TestQuarantineRestoreDistinguishesGoneFromAlreadyRestored(t *testing.T) {
	t.Parallel()

	t.Run("already restored says so", func(t *testing.T) {
		t.Parallel()
		s, _, r := setupQuarantine(t)
		seedHeld(t, s, "h1", 6, nil)
		doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", "")

		w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/h1/restore", "")
		typ, msg := flashFrom(t, w)
		if typ == "error" {
			t.Errorf("flashed an error (%q) for a submission that was restored", msg)
		}
		if !strings.Contains(strings.ToLower(msg), "already restored") {
			t.Errorf("flash = %q, want it to say already restored", msg)
		}
	})

	t.Run("a purged submission is not reported as delivered", func(t *testing.T) {
		t.Parallel()
		s, _, r := setupQuarantine(t)
		seedHeld(t, s, "gone", 6, nil)
		if _, err := s.DeleteAllHeld(); err != nil {
			t.Fatalf("DeleteAllHeld: %v", err)
		}

		w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/gone/restore", "")
		typ, msg := flashFrom(t, w)
		if typ == "success" {
			t.Errorf("flashed success (%q) for a submission that no longer exists — "+
				"the operator will look for it in an inbox it will never reach", msg)
		}
		if strings.Contains(strings.ToLower(msg), "already restored") {
			t.Errorf("flash = %q, want it to say the submission is gone, not restored", msg)
		}
	})

	t.Run("an id that never existed is not reported as delivered", func(t *testing.T) {
		t.Parallel()
		s, _, r := setupQuarantine(t)
		w := doAdminRequest(t, s, r, "POST", "/admin/quarantine/never-existed/restore", "")
		typ, msg := flashFrom(t, w)
		if typ == "success" {
			t.Errorf("flashed success (%q) for an id that never existed", msg)
		}
	})
}

// senderLabel must name the same person on every render. It used to range the
// data map twice, so a submission carrying both "name" and "Name" — legal,
// since only ambiguity in the email field is rejected — showed a different
// sender each time the queue was refreshed, and a submission with none of the
// preferred keys showed a wholly arbitrary field value.
func TestSenderLabelIsDeterministic(t *testing.T) {
	t.Parallel()

	cases := []map[string]string{
		{"name": "Alpha", "Name": "Beta", "message": "hi"},
		{"From": "a@x.com", "from": "b@y.com"},
		{"zz": "last", "aa": "first", "mm": "middle"},
		{"Subject": "S", "subject": "T", "SUBJECT": "U"},
	}
	for _, data := range cases {
		first := senderLabel(data)
		for i := 0; i < 200; i++ {
			if got := senderLabel(data); got != first {
				t.Fatalf("senderLabel(%v) returned %q then %q — the queue would reshuffle per render", data, first, got)
			}
		}
	}
}

// TestRulesPageSurfacesDeadRules covers the wiring, not the rendering.
//
// The template test asserts rules.html renders a Problems block when given one.
// Nothing asserted that RulesPage actually calls CheckRules and passes the
// result — so replacing that call with an empty slice silently removed the whole
// user-facing point of the feature, with every test still green.
func TestRulesPageSurfacesDeadRules(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)

	// A rule the current matcher can never produce. Written straight to the
	// table, because AddFilterRule would reject it — which is the situation: it
	// was stored by a binary whose validator accepted it.
	if _, err := s.DB().Exec(
		`INSERT INTO filter_rules (id, kind, type, value, note, hits, created_at)
		 VALUES ('legacy1','block','email','bot@localhost','',0,datetime('now'))`); err != nil {
		t.Fatalf("seeding the legacy rule: %v", err)
	}

	body := doAdminRequest(t, s, r, "GET", "/admin/rules", "").Body.String()

	// Assert on the warning, not on the rule value: the value also appears in
	// the block-list table below, so checking for it passes whether or not
	// CheckRules ran at all. The first version of this test did exactly that,
	// and a mutant removing the CheckRules call survived it.
	if !strings.Contains(body, "can never match") {
		t.Errorf("the rules page rendered no warning for a rule that cannot fire; "+
			"an operator would see it listed as active and believe they were "+
			"protected.\nbody = %q", body)
	}
	if !strings.Contains(body, "bot@localhost") {
		t.Error("the warning did not name which rule is dead")
	}
}

// A store failure must not read as a clean bill of health. "No problems found"
// and "I could not look" are different statements, and only one of them is true
// when the query failed.
func TestRulesPageDoesNotClaimHealthWhenItCannotLook(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)
	cookie := loginCookie(t, s)

	if _, err := s.DB().Exec("DROP TABLE filter_rules"); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}

	w := doAdminRequestAs(t, cookie, r, "GET", "/admin/rules", "")
	if w.Code == http.StatusOK {
		t.Errorf("status = 200 with an unreadable rules table; the page would render " +
			"an empty, problem-free rule list that the operator has no reason to doubt")
	}
}

// TestAddRuleKeepsTheNote covers a field that existed end to end except for the
// one place it had to start: the input.
//
// AddRule reads r.PostFormValue("note"), the store persists it, and rules.html
// renders it under each allow entry — but none of the three forms on that page
// posted a note, so it was always empty and the render was dead markup. A column,
// a parameter and a template branch, none of them reachable.
func TestAddRuleKeepsTheNote(t *testing.T) {
	t.Parallel()
	s, _, r := setupQuarantine(t)

	form := url.Values{
		"kind":  {"allow"},
		"type":  {"email"},
		"value": {"vip@customer.com"},
		"note":  {"renewal contact, do not filter"},
	}
	if w := doAdminRequest(t, s, r, "POST", "/admin/rules", form.Encode()); w.Code != http.StatusSeeOther {
		t.Fatalf("add status = %d, want 303", w.Code)
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	var found bool
	for _, rule := range rules {
		if rule.Value == "vip@customer.com" {
			found = true
			if rule.Note != "renewal contact, do not filter" {
				t.Errorf("stored note = %q, want the submitted one.\nAn allow rule "+
					"skips every check; six months on, the note is the only record of "+
					"why it exists.", rule.Note)
			}
		}
	}
	if !found {
		t.Fatal("the allow rule was not stored at all")
	}
}

// TestRulesPageOffersANoteWhereItRendersOne is the other half: the real template
// must both collect the note and show it. Asserted against templates/rules.html
// rather than the stub, because the stub is what made this invisible — it
// renders neither, so every existing test passed with the input missing.
func TestRulesPageOffersANoteWhereItRendersOne(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(templateDir, "rules.html"))
	if err != nil {
		t.Fatalf("read rules.html: %v", err)
	}
	src := string(body)

	if !strings.Contains(src, "{{.Note}}") {
		t.Fatal("rules.html renders no note anywhere; if the field is dead, remove " +
			"the column and the handler's read of it rather than leaving three " +
			"pieces of a feature lying around")
	}
	if !strings.Contains(src, `name="note"`) {
		t.Error("rules.html renders {{.Note}} but no form on the page posts a note, " +
			"so it is always empty and the render is dead markup")
	}
}
