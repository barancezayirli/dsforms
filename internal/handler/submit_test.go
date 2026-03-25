package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/mail"
	"github.com/youruser/dsforms/internal/store"
)

func setupSubmit(t *testing.T) (*store.Store, *mail.MockMailer, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	m := mail.NewMockMailer()
	h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com"}
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
	h := &SubmitHandler{Store: s, Notifier: m, BaseURL: "https://example.com"}
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
