package handler

import (
	"html/template"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

// The overview template stands in for base.html's degraded banner. It renders
// the same {{if .Degraded}} the real shell does, which is the point: the bug
// this file exists to catch was a field-resolution bug, invisible to any test
// that read the struct instead of executing a template against it.
const overviewTestTemplate = `{{define "content"}}` +
	`{{if .Degraded}}<span class="degraded">some data could not be loaded</span>{{end}}` +
	`{{range .KPIs}}<span class="kpi">{{.Label}}:{{.Value}}</span>{{end}}` +
	`<span class="greeting">{{.Greeting}}</span>` +
	`{{end}}`

func setupOverview(t *testing.T) (*store.Store, *chi.Mux) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "me@example.com"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	return s, overviewRouter(t, s)
}

func overviewRouter(t *testing.T, s *store.Store) *chi.Mux {
	t.Helper()
	base := template.Must(template.New("base").Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	page := template.Must(template.Must(base.Clone()).Parse(overviewTestTemplate))

	h := &OverviewHandler{
		Store: s,
		Base: Base{
			Nav:       s,
			SecretKey: testSecretKey,
			BaseURL:   "https://example.com",
			Templates: map[string]*template.Template{"home.html": page},
		},
		Limiter:       ratelimit.NewLimiter(5, 5, time.Now),
		RateBurst:     5,
		RatePerMinute: 5,
		RetentionDays: 30,
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin", h.Page)
	})
	return r
}

func TestOverviewRendersHealthy(t *testing.T) {
	t.Parallel()
	s, r := setupOverview(t)

	w := doAdminRequest(t, s, r, "GET", "/admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="greeting"`) {
		t.Errorf("the page did not render: %q", body)
	}

	// The load-bearing half. Without this a hard-coded Degraded = true would
	// satisfy the failure test below and nothing would notice.
	if strings.Contains(body, `class="degraded"`) {
		t.Error("a healthy instance rendered the degraded banner")
	}
}

// TestOverviewSaysSoWhenDegraded is the regression test for a banner that could
// not fire.
//
// overviewData embedded PageData (which carries Degraded) and *also* declared
// its own Degraded field. Go promotes the shallower field, and html/template
// resolves the same way, so base.html's {{if .Degraded}} bound to the local one
// and never saw the embedded one. The merge line wrote into the field the
// template could not reach.
//
// The consequence was worst in exactly the case the feature was built for: when
// the shell's own NavCounts query failed and every panel query succeeded, the
// page rendered zeroed badges, a zeroed Unread KPI and a zeroed quarantine
// line — a database with a full inbox looking pixel-identical to a fresh
// install — with nothing to say so.
func TestOverviewSaysSoWhenDegraded(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateForm(store.Form{ID: "f1", Name: "Contact"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	r := overviewRouter(t, s)

	// A session has to exist before the database is broken, since the request
	// must reach the handler rather than being bounced by auth.
	cookie := loginCookie(t, s)

	// waitlist_entries is read by NavCounts and by no overview panel, so
	// dropping it fails the *shell's* query while every panel query still
	// succeeds. That isolation is the whole test: a panel failure sets the
	// struct's own field and would have rendered a banner even with the bug
	// present, so breaking a panel proves nothing here.
	if _, err := s.DB().Exec("DROP TABLE waitlist_entries"); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}

	w := doAdminRequestAs(t, cookie, r, "GET", "/admin", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a degraded page must still render", w.Code)
	}
	if !strings.Contains(w.Body.String(), `class="degraded"`) {
		t.Errorf("a failing query rendered no degraded banner; body = %q", w.Body.String())
	}
}
