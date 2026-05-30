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

func setupWaitlistSubmit(t *testing.T) (*store.Store, *mail.MockMailer, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	m := mail.NewMockMailer()
	h := &WaitlistSubmitHandler{Store: s, Mailer: m, BaseURL: "https://example.com"}
	r := chi.NewRouter()
	r.Post("/w/{waitlistID}", h.Handle)
	_ = s.CreateWaitlist(store.Waitlist{
		ID: "wl", Name: "Launch", Redirect: "https://example.com/joined",
		ConfirmSubject: "You're in", ConfirmBody: "Hi {{email}}, #{{position}}",
	})
	return s, m, r
}

func postWaitlist(r *chi.Mux, id string, form url.Values, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/w/"+id, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWaitlistSubmitUnknown(t *testing.T) {
	t.Parallel()
	_, _, r := setupWaitlistSubmit(t)
	w := postWaitlist(r, "nope", url.Values{"email": {"a@x.com"}}, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestWaitlistSubmitMissingEmail(t *testing.T) {
	t.Parallel()
	_, _, r := setupWaitlistSubmit(t)
	w := postWaitlist(r, "wl", url.Values{"name": {"Al"}}, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWaitlistSubmitInvalidEmail(t *testing.T) {
	t.Parallel()
	_, _, r := setupWaitlistSubmit(t)
	w := postWaitlist(r, "wl", url.Values{"email": {"not-an-email"}}, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWaitlistSubmitJSONSuccess(t *testing.T) {
	t.Parallel()
	s, m, r := setupWaitlistSubmit(t)
	w := postWaitlist(r, "wl", url.Values{"email": {"a@x.com"}, "name": {"Al"}}, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Success       bool `json:"success"`
		Position      int  `json:"position"`
		AlreadyJoined bool `json:"already_joined"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if !resp.Success || resp.Position != 1 || resp.AlreadyJoined {
		t.Errorf("resp = %+v, want success/pos1/notjoined", resp)
	}
	n, _ := s.CountEntries("wl")
	if n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
	if !m.Wait(time.Second) || m.SendMailCount() != 1 {
		t.Errorf("expected one confirmation email, got %d", m.SendMailCount())
	}
	if m.SentMails[0].To != "a@x.com" || !strings.Contains(m.SentMails[0].Body, "#1") {
		t.Errorf("confirmation mail = %+v, want To a@x.com body with #1", m.SentMails[0])
	}
}

func TestWaitlistSubmitDuplicateNoSecondEmail(t *testing.T) {
	t.Parallel()
	_, m, r := setupWaitlistSubmit(t)
	_ = postWaitlist(r, "wl", url.Values{"email": {"a@x.com"}}, "application/json")
	m.Wait(time.Second)
	w := postWaitlist(r, "wl", url.Values{"email": {"a@x.com"}}, "application/json")
	var resp struct {
		Position      int  `json:"position"`
		AlreadyJoined bool `json:"already_joined"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.AlreadyJoined || resp.Position != 1 {
		t.Errorf("resp = %+v, want alreadyJoined/pos1", resp)
	}
	if m.SendMailCount() != 1 {
		t.Errorf("SendMailCount = %d, want 1 (no email on duplicate)", m.SendMailCount())
	}
}

func TestWaitlistSubmitRedirectIncludesPosition(t *testing.T) {
	t.Parallel()
	_, _, r := setupWaitlistSubmit(t)
	w := postWaitlist(r, "wl", url.Values{"email": {"a@x.com"}}, "")
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "position=1") {
		t.Errorf("Location = %q, want to contain position=1", loc)
	}
}

func TestWaitlistSubmitHoneypot(t *testing.T) {
	t.Parallel()
	s, _, r := setupWaitlistSubmit(t)
	w := postWaitlist(r, "wl", url.Values{"email": {"a@x.com"}, "_honeypot": {"bot"}}, "application/json")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	n, _ := s.CountEntries("wl")
	if n != 0 {
		t.Errorf("entries = %d, want 0 (honeypot)", n)
	}
}

func TestWaitlistSubmitNormalizesEmail(t *testing.T) {
	t.Parallel()
	s, _, r := setupWaitlistSubmit(t)
	form := url.Values{"email": {"Al <al@x.com>"}}
	w := postWaitlist(r, "wl", form, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	entries, err := s.ListEntries("wl")
	if err != nil {
		t.Fatalf("ListEntries error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Email != "al@x.com" {
		t.Errorf("stored email = %q, want normalized al@x.com", entries[0].Email)
	}
}
