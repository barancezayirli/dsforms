package handler

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

var _ = context.Background // keep context import (used by later tasks' helpers)
