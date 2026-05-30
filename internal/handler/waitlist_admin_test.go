package handler

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/store"
)

// testTemplates parses the real templates the same way main.go does.
func testTemplates(t *testing.T) map[string]*template.Template {
	t.Helper()
	funcMap := template.FuncMap{"add": func(a, b int) int { return a + b }}
	base, err := template.New("base").Funcs(funcMap).ParseFiles("../../templates/base.html")
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	tmpls := map[string]*template.Template{}
	for _, name := range []string{"waitlists.html", "waitlist_new.html", "waitlist_edit.html", "waitlist_detail.html", "broadcast_new.html", "broadcast_detail.html"} {
		c, err := base.Clone()
		if err != nil {
			t.Fatalf("clone: %v", err)
		}
		if _, err := c.ParseFiles("../../templates/" + name); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		tmpls[name] = c
	}
	return tmpls
}

type nopNotifier struct{}

func (nopNotifier) Notify() {}

func setupWaitlistAdmin(t *testing.T) (*store.Store, *WaitlistHandler) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := &WaitlistHandler{
		Store:       s,
		SecretKey:   "test-secret",
		BaseURL:     "https://example.com",
		Templates:   testTemplates(t),
		Broadcaster: nopNotifier{},
	}
	return s, h
}

// withUser injects an authenticated user into the request context.
func withUser(req *http.Request) *http.Request {
	ctx := auth.WithUser(req.Context(), store.User{ID: "u1", Username: "admin"})
	return req.WithContext(ctx)
}

// withURLParam injects a chi URL param into the request context (accumulates).
func withURLParam(req *http.Request, key, val string) *http.Request {
	rctx, _ := req.Context().Value(chi.RouteCtxKey).(*chi.Context)
	if rctx == nil {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestWaitlistListPage(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch List"})

	req := withUser(httptest.NewRequest("GET", "/admin/waitlists", nil))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Launch List") {
		t.Error("body should contain the waitlist name")
	}
}

func TestWaitlistCreate(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)

	form := url.Values{"name": {"My List"}, "redirect": {"https://x.com/ok"}}
	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/new", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	list, _ := s.ListWaitlists()
	if len(list) != 1 || list[0].Name != "My List" {
		t.Errorf("waitlists = %+v, want one named My List", list)
	}
}

func TestWaitlistCreateRequiresName(t *testing.T) {
	t.Parallel()
	_, h := setupWaitlistAdmin(t)
	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/new", strings.NewReader("name=")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Create(w, req)
	if w.Code != http.StatusOK { // re-renders form with error
		t.Errorf("status = %d, want 200 (re-render)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "name is required") {
		t.Error("expected validation error in body")
	}
}

func TestWaitlistEditPage(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch List"})

	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wl/edit", nil))
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.EditPage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Launch List") {
		t.Error("body should show the waitlist name")
	}
	if !strings.Contains(body, "/w/wl") {
		t.Error("body should show the public endpoint snippet with /w/{id}")
	}
}

func TestWaitlistUpdate(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Old"})

	form := url.Values{"name": {"New Name"}, "confirm_subject": {"Hi"}}
	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/wl/edit", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.Edit(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	got, _ := s.GetWaitlist("wl")
	if got.Name != "New Name" || got.ConfirmSubject != "Hi" {
		t.Errorf("after update = %+v, want New Name / Hi", got)
	}
}

func TestWaitlistDelete(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "X"})

	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/wl/delete", nil))
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if _, err := s.GetWaitlist("wl"); err == nil {
		t.Error("waitlist should be deleted")
	}
}

func TestWaitlistDetailShowsEntries(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`})

	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wl", nil))
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.Detail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "a@x.com") {
		t.Error("body should list the entry email")
	}
}

func TestWaitlistEntryDelete(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`})

	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/wl/entries/e1/delete", nil))
	req = withURLParam(req, "id", "wl")
	req = withURLParam(req, "entryID", "e1")
	w := httptest.NewRecorder()
	h.DeleteEntry(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	n, _ := s.CountEntries("wl")
	if n != 0 {
		t.Errorf("entries = %d, want 0", n)
	}
}

func TestWaitlistExportCSV(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{"name":"Al"}`})

	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wl/export", nil))
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.ExportCSV(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "email") || !strings.Contains(body, "a@x.com") {
		t.Errorf("CSV missing expected content: %q", body)
	}
}

func TestWaitlistExportCSVSanitizesFormulas(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	// Malicious extra field value starting with '=' (formula trigger).
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{"note":"=cmd|'/c calc'!A1"}`})

	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wl/export", nil))
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.ExportCSV(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// The dangerous value must be neutralized with a leading apostrophe.
	if !strings.Contains(body, "'=cmd") {
		t.Errorf("formula not sanitized; CSV body = %q", body)
	}
}

func TestCSVSafe(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"=danger": "'=danger",
		"+danger": "'+danger",
		"-danger": "'-danger",
		"@danger": "'@danger",
		"safe":    "safe",
		"":        "",
		"a=b":     "a=b",
	}
	for in, want := range cases {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

// recordingNotifier counts Notify calls.
type recordingNotifier struct{ calls int }

func (n *recordingNotifier) Notify() { n.calls++ }

func TestBroadcastCreateEnqueuesAndNotifies(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	notifier := &recordingNotifier{}
	h.Broadcaster = notifier
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`})
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e2", WaitlistID: "wl", Email: "b@x.com", RawData: `{}`})

	form := url.Values{"subject": {"We launched"}, "body": {"Come back"}}
	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/wl/broadcast", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.CreateBroadcast(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if notifier.calls != 1 {
		t.Errorf("Notify calls = %d, want 1", notifier.calls)
	}
	pending, _ := s.NextPendingDeliveries(10)
	if len(pending) != 2 {
		t.Errorf("pending deliveries = %d, want 2", len(pending))
	}
}

func TestBroadcastCreateRequiresSubjectAndBody(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	_, _, _ = s.CreateEntry(store.WaitlistEntry{ID: "e1", WaitlistID: "wl", Email: "a@x.com", RawData: `{}`})

	form := url.Values{"subject": {""}, "body": {"x"}}
	req := withUser(httptest.NewRequest("POST", "/admin/waitlists/wl/broadcast", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withURLParam(req, "id", "wl")
	w := httptest.NewRecorder()
	h.CreateBroadcast(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render with error)", w.Code)
	}
	pending, _ := s.NextPendingDeliveries(10)
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 (nothing enqueued on validation error)", len(pending))
	}
}

func TestBroadcastDetailShowsCounts(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	_ = s.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"})
	_ = s.CreateBroadcast(store.Broadcast{ID: "b1", WaitlistID: "wl", Subject: "S", Body: "B"}, []string{"a@x.com"})

	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wl/broadcasts/b1", nil))
	req = withURLParam(req, "id", "wl")
	req = withURLParam(req, "bid", "b1")
	w := httptest.NewRecorder()
	h.BroadcastDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Pending") {
		t.Error("body should show delivery status counts")
	}
}

func TestBroadcastDetailUnknownReturns404(t *testing.T) {
	t.Parallel()
	_, h := setupWaitlistAdmin(t)
	// Need a real waitlist so getWaitlistOr404 passes; the broadcast id is bogus.
	if err := h.Store.CreateWaitlist(store.Waitlist{ID: "wl", Name: "Launch"}); err != nil {
		t.Fatalf("seed waitlist: %v", err)
	}
	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wl/broadcasts/nope", nil))
	req = withURLParam(req, "id", "wl")
	req = withURLParam(req, "bid", "nope")
	w := httptest.NewRecorder()
	h.BroadcastDetail(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown broadcast", w.Code)
	}
}

func TestBroadcastDetailWrongWaitlistReturns404(t *testing.T) {
	t.Parallel()
	s, h := setupWaitlistAdmin(t)
	if err := s.CreateWaitlist(store.Waitlist{ID: "wlA", Name: "A"}); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := s.CreateWaitlist(store.Waitlist{ID: "wlB", Name: "B"}); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	// Broadcast belongs to wlB.
	if err := s.CreateBroadcast(store.Broadcast{ID: "bB", WaitlistID: "wlB", Subject: "S", Body: "B"}, []string{"a@x.com"}); err != nil {
		t.Fatalf("seed broadcast: %v", err)
	}
	// Request it under wlA's URL → must 404.
	req := withUser(httptest.NewRequest("GET", "/admin/waitlists/wlA/broadcasts/bB", nil))
	req = withURLParam(req, "id", "wlA")
	req = withURLParam(req, "bid", "bB")
	w := httptest.NewRecorder()
	h.BroadcastDetail(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for cross-waitlist broadcast", w.Code)
	}
}

