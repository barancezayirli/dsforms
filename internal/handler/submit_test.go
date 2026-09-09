package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/mail"
	"github.com/barancezayirli/dsforms/internal/spam"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

type mockWebhookSender struct {
	mu    sync.Mutex
	calls []store.Form
	ch    chan struct{}
}

func newMockWebhookSender() *mockWebhookSender {
	return &mockWebhookSender{ch: make(chan struct{}, 10)}
}

func (m *mockWebhookSender) Send(form store.Form, sub store.Submission) error {
	m.mu.Lock()
	m.calls = append(m.calls, form)
	m.mu.Unlock()
	m.ch <- struct{}{}
	return nil
}

func (m *mockWebhookSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockWebhookSender) wait(timeout time.Duration) bool {
	select {
	case <-m.ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

func setupSubmit(t *testing.T) (*store.Store, *mail.MockMailer, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	m := mail.NewMockMailer()
	h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)
	_ = s.CreateForm(store.Form{ID: "test-form", Name: "Test", EmailTo: "test@example.com", Redirect: "https://example.com/thanks"})
	return s, m, r
}

func TestSubmitUnknownForm(t *testing.T) {
	t.Parallel()
	_, _, r := setupSubmit(t)
	req := httptest.NewRequest("POST", "/f/nonexistent", strings.NewReader("name=test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSubmitHoneypotIgnored(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"name": {"test"}, "_honeypot": {"bot-value"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (honeypot)", len(subs))
	}
	// The honeypot is the one path that still drops rather than holds. Asserting
	// only an empty inbox would pass just as happily if it started quarantining
	// instead — which would bury the review queue under the highest-volume bot
	// traffic on the instance.
	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("honeypot hits must be dropped, not held: %v", held)
	}
}

func TestSubmitValidStoresSubmission(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}, "email": {"alice@example.com"}, "message": {"Hello"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 1 {
		t.Fatalf("submissions = %d, want 1", len(subs))
	}
	if subs[0].Data["name"] != "Alice" {
		t.Errorf("Data[name] = %q, want Alice", subs[0].Data["name"])
	}
}

func TestSubmitTriggersEmail(t *testing.T) {
	t.Parallel()
	_, m, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !m.Wait(2 * time.Second) {
		t.Fatal("email not sent within timeout")
	}
	if m.CallCount() != 1 {
		t.Errorf("email calls = %d, want 1", m.CallCount())
	}
}

// TestSubmitNotificationCarriesCreatedAt guards the notification timestamp. The
// submission handed to the Notifier is the in-memory struct, not a re-read row,
// so leaving CreatedAt to the DB default shipped a zero time.Time to the mailer
// and put "Date: Mon, 01 Jan 0001 00:00:00 +0000" on every notification email
// while the admin UI showed the correct time.
func TestSubmitNotificationCarriesCreatedAt(t *testing.T) {
	t.Parallel()
	s, m, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !m.Wait(2 * time.Second) {
		t.Fatal("email not sent within timeout")
	}
	notified := m.Calls[0].Sub
	if notified.CreatedAt.IsZero() {
		t.Fatal("notified submission has zero CreatedAt; email Date header would read 01 Jan 0001")
	}
	subs, err := s.ListSubmissions("test-form")
	if err != nil {
		t.Fatalf("ListSubmissions error = %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("submissions = %d, want 1", len(subs))
	}
	if !notified.CreatedAt.Equal(subs[0].CreatedAt) {
		t.Errorf("notified CreatedAt = %v, stored CreatedAt = %v; email and admin UI must agree",
			notified.CreatedAt, subs[0].CreatedAt)
	}
}

func TestSubmitRedirectOverride(t *testing.T) {
	t.Parallel()
	_, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}, "_redirect": {"https://other.com/done"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://other.com/done" {
		t.Errorf("Location = %q, want https://other.com/done", loc)
	}
}

func TestSubmitFormRedirectUsed(t *testing.T) {
	t.Parallel()
	_, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if loc := w.Header().Get("Location"); loc != "https://example.com/thanks" {
		t.Errorf("Location = %q, want https://example.com/thanks", loc)
	}
}

func TestSubmitDefaultRedirect(t *testing.T) {
	t.Parallel()
	s, m, _ := setupSubmit(t)
	_ = s.CreateForm(store.Form{ID: "no-redir", Name: "NoRedir", EmailTo: "t@t.com"})
	h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/no-redir", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if loc := w.Header().Get("Location"); loc != "/success" {
		t.Errorf("Location = %q, want /success", loc)
	}
}

func TestSubmitJSONResponse(t *testing.T) {
	t.Parallel()
	_, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	if !resp["success"] {
		t.Error("success = false, want true")
	}
}

func TestSubmitEmptyFieldsReturns400(t *testing.T) {
	t.Parallel()
	_, _, r := setupSubmit(t)
	form := url.Values{"_redirect": {"https://x.com"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSubmitInvalidEmailReturns400(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"email": {"' ORDER BY 1-- -"}, "message": {"hi"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (invalid email must not be stored)", len(subs))
	}
}

func TestSubmitInvalidEmailCaseInsensitiveField(t *testing.T) {
	t.Parallel()
	_, _, r := setupSubmit(t)
	form := url.Values{"Email": {"not-an-email"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestSubmitTwoEmailFieldsAlwaysRejected pins the ambiguous-sender rejection,
// and pins it as *deterministic*.
//
// The previous implementation ranged the data map and returned on the first key
// that case-insensitively matched "email", so which of two such fields decided
// the request came down to Go's randomised map iteration: the same submission
// was a 400 or a 200 depending on the run. That nondeterminism was the visible
// half of a security bug — the same two-field trick unlocked an allow rule and
// skipped the spam filter entirely.
//
// One iteration proves nothing here, so this repeats: a "first key wins"
// regression would show up as an occasional 201, not a consistent one.
func TestSubmitTwoEmailFieldsAlwaysRejected(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)

	for i := 0; i < 50; i++ {
		form := url.Values{
			"email":   {"mallory@spam.example"},
			"Email":   {"vip@customer.com"},
			"message": {"hi"},
		}
		req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("run %d: status = %d, want 400 for two email fields", i, w.Code)
		}
	}

	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (an ambiguous sender must not be stored)", len(subs))
	}
}

func TestSubmitNoEmailFieldUnaffected(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}, "message": {"hi"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (no email field must not be validated)", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 1 {
		t.Errorf("submissions = %d, want 1", len(subs))
	}
}

func TestSubmitXForwardedFor(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 192.168.1.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) == 0 {
		t.Fatal("no submissions")
	}
	if subs[0].IP != "10.0.0.1" {
		t.Errorf("IP = %q, want 10.0.0.1", subs[0].IP)
	}
}

func TestSubmitXRealIP(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", "10.0.0.2")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	subs, _ := s.ListSubmissions("test-form")
	if subs[0].IP != "10.0.0.2" {
		t.Errorf("IP = %q, want 10.0.0.2", subs[0].IP)
	}
}

func TestSubmitRemoteAddrFallback(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	subs, _ := s.ListSubmissions("test-form")
	if subs[0].IP != "192.0.2.1" {
		t.Errorf("IP = %q, want 192.0.2.1", subs[0].IP)
	}
}

func TestSubmitInternalFieldsNotStored(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{
		"name":      {"Alice"},
		"_honeypot": {""},
		"_redirect": {"https://x.com"},
		"_subject":  {"Custom Subject"},
	}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) == 0 {
		t.Fatal("no submissions")
	}
	for _, key := range []string{"_honeypot", "_redirect", "_subject"} {
		if _, ok := subs[0].Data[key]; ok {
			t.Errorf("internal field %q should not be stored", key)
		}
	}
}

func TestExtractIPXForwardedFor(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 70.41.3.18")
	if got := ExtractIP(req); got != "203.0.113.5" {
		t.Errorf("ExtractIP = %q, want 203.0.113.5", got)
	}
}

func TestExtractIPXRealIP(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "203.0.113.6")
	if got := ExtractIP(req); got != "203.0.113.6" {
		t.Errorf("ExtractIP = %q, want 203.0.113.6", got)
	}
}

func TestExtractIPRemoteAddr(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	// httptest sets RemoteAddr to "192.0.2.1:1234"
	if got := ExtractIP(req); got != "192.0.2.1" {
		t.Errorf("ExtractIP = %q, want 192.0.2.1", got)
	}
}

func TestDetermineRedirectFormValue(t *testing.T) {
	t.Parallel()
	got := determineRedirect("https://custom.com", "https://form.com")
	if got != "https://custom.com" {
		t.Errorf("determineRedirect = %q, want https://custom.com", got)
	}
}

func TestDetermineRedirectFormDefault(t *testing.T) {
	t.Parallel()
	got := determineRedirect("", "https://form.com")
	if got != "https://form.com" {
		t.Errorf("determineRedirect = %q, want https://form.com", got)
	}
}

func TestDetermineRedirectFallback(t *testing.T) {
	t.Parallel()
	got := determineRedirect("", "")
	if got != "/success" {
		t.Errorf("determineRedirect = %q, want /success", got)
	}
}

func TestSubmitWebhookFired(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	_ = s.CreateForm(store.Form{
		ID: "wh-form", Name: "WH", EmailTo: "test@test.com",
		WebhookURL: "https://hooks.example.com", WebhookFormat: "generic",
	})
	m := mail.NewMockMailer()
	wh := newMockWebhookSender()
	h := &SubmitHandler{Store: s, Notifier: m, Webhook: wh, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)

	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/wh-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !wh.wait(2 * time.Second) {
		t.Fatal("webhook not called within timeout")
	}
	if wh.callCount() != 1 {
		t.Errorf("webhook calls = %d, want 1", wh.callCount())
	}
}

func TestSubmitNoWebhook(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	_ = s.CreateForm(store.Form{ID: "no-wh", Name: "NoWH", EmailTo: "test@test.com"})
	m := mail.NewMockMailer()
	wh := newMockWebhookSender()
	h := &SubmitHandler{Store: s, Notifier: m, Webhook: wh, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)

	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/no-wh", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Wait for email to fire (proves goroutine ran)
	if !m.Wait(2 * time.Second) {
		t.Fatal("email not sent")
	}
	// Webhook should NOT have been called
	if wh.callCount() != 0 {
		t.Errorf("webhook calls = %d, want 0", wh.callCount())
	}
}

func TestSubmitSpamHeld(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{
		"name":    {"bot"},
		"message": {"casino deals, buy backlinks now"},
	}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// The response still looks like success — a bot must not learn it was
	// caught — but the submission is now held for review rather than binned.
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (indistinguishable from success)", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (spam kept out of the inbox)", len(subs))
	}
	// Asserting the inbox is empty is not enough on its own: that would also
	// hold if quarantine were reverted to a silent drop.
	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1 — spam must be recoverable, not discarded", len(held))
	}
}

func TestSubmitSpamHeldJSON(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{
		"name":    {"bot"},
		"message": {"casino deals, buy backlinks now"},
	}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (JSON response indistinguishable from success)", w.Code)
	}
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if !body["success"] {
		t.Errorf("body = %v, want success=true", body)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (spam kept out of the inbox)", len(subs))
	}
	// As with the non-JSON twin: an empty inbox alone would also hold if
	// quarantine were reverted to a silent drop.
	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Errorf("held = %d, want 1 — spam must be recoverable, not discarded", len(held))
	}
}

func TestSubmitHamWithOneLinkStored(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)
	form := url.Values{
		"name":    {"Jane Doe"},
		"message": {"Loved the talk! Slides at https://example.com — thanks."},
	}
	req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 1 {
		t.Errorf("submissions = %d, want 1 (ham stored)", len(subs))
	}
}

func TestSubmitNoEmailNoWebhook(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	_ = s.CreateForm(store.Form{ID: "silent", Name: "Silent"})
	h := &SubmitHandler{Store: s, Notifier: nil, Webhook: nil, BaseURL: "https://example.com", Tracker: spam.NewTracker(1000)}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)

	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/silent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("silent")
	if len(subs) != 1 {
		t.Errorf("submissions = %d, want 1", len(subs))
	}
}

func TestSubmitThirdSameIPHeld(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)

	submit := func() *httptest.ResponseRecorder {
		form := url.Values{"name": {"Alice"}, "message": {"hello there"}}
		req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	submit()
	submit()
	w := submit()

	if w.Code != http.StatusFound {
		t.Errorf("3rd submission status = %d, want 302 (indistinguishable from success)", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 2 {
		t.Errorf("submissions = %d, want 2 (1st and 2nd accepted, 3rd held)", len(subs))
	}
	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1 — the 3rd is quarantined, not discarded", len(held))
	}
}

// TestSubmitContentSpamStillCountsTowardIPRepeat pins the invariant that
// Tracker.Seen runs on every submission, including ones already caught by
// content scoring. If Seen were moved inside the scoring branch, the two spam
// submissions below would never be counted, and the 3rd — clean content from
// the same IP — would be accepted instead of held.
func TestSubmitContentSpamStillCountsTowardIPRepeat(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)

	submit := func(message string) *httptest.ResponseRecorder {
		form := url.Values{"name": {"Alice"}, "message": {message}}
		req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "203.0.113.44")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1st and 2nd: markup-link spam — an instant drop on content score alone.
	submit(`<a href="http://x.com">click</a>`)
	submit(`<a href="http://y.com">click</a>`)
	// 3rd: perfectly clean content, same IP — must still be held as a repeat.
	w := submit("hello there, loved the talk")

	if w.Code != http.StatusFound {
		t.Errorf("3rd submission status = %d, want 302 (indistinguishable from success)", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 0 {
		t.Errorf("submissions = %d, want 0 (2 spam held, 3rd held as IP repeat)", len(subs))
	}
	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 3 {
		t.Errorf("held = %d, want all 3 recoverable", len(held))
	}
}

func TestSubmitSecondSameIPStillStored(t *testing.T) {
	t.Parallel()
	s, _, r := setupSubmit(t)

	submit := func() *httptest.ResponseRecorder {
		form := url.Values{"name": {"Alice"}, "message": {"hello there"}}
		req := httptest.NewRequest("POST", "/f/test-form", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	submit()
	w := submit()

	if w.Code != http.StatusFound {
		t.Errorf("2nd submission status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("test-form")
	if len(subs) != 2 {
		t.Errorf("submissions = %d, want 2 (repeat threshold is 3rd, not 2nd)", len(subs))
	}
}
