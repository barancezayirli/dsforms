package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barancezayirli/dsforms/internal/config"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

// testRouter builds the real route table against a temporary database.
//
// No mailer and no webhook sender: both are nil in a default deployment too, and
// the routes must not depend on them being present.
func testRouter(t *testing.T) (*chi.Mux, *store.Store) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "routes.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	return routes(serverDeps{
		store:     s,
		cfg:       config.Config{DBPath: dbPath, BaseURL: "https://example.com", SecretKey: strings.Repeat("a", 32), RateBurst: 100, RatePerMinute: 600},
		templates: templates,
	}), s
}

// TestRoutesServeTheStyledErrorPages pins the production wiring of the error
// pages, not just newRouter's ability to render them.
//
// The styled-page tests in main_test.go build their own router, so nothing
// checked that routes() hands its templates over. Passing nil there served
// plain-text 404s and 500s from the real route table with the whole suite green.
func TestRoutesServeTheStyledErrorPages(t *testing.T) {
	t.Parallel()
	r, _ := testRouter(t)
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })

	tests := []struct {
		name       string
		path       string
		wantStatus int
		want       string
	}{
		{"404", "/definitely-not-a-route", http.StatusNotFound, "Nothing here"},
		{"500", "/boom", http.StatusInternalServerError, "Something went wrong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", tt.path, nil))

			if w.Code != tt.wantStatus {
				t.Errorf("GET %s status = %d, want %d", tt.path, w.Code, tt.wantStatus)
			}
			if !strings.Contains(w.Body.String(), tt.want) {
				t.Errorf("GET %s did not serve the styled page (no %q); got %.160q",
					tt.path, tt.want, w.Body.String())
			}
		})
	}
}

// TestEveryAdminRouteRequiresAuth is the one this extraction exists for.
//
// Every route registration lived inline in main(), which no test could call — so
// nothing could catch a route registered outside the RequireAuth group. That is
// an authentication bypass, and it would be invisible: the page would render
// perfectly, for anyone.
//
// Walked from the real chi route table rather than a list written here, because
// a list is exactly what a new route gets left out of.
func TestEveryAdminRouteRequiresAuth(t *testing.T) {
	t.Parallel()
	r, _ := testRouter(t)

	// Reachable without a session by design: the login form itself, the static
	// assets the login page needs, health, and the public submit endpoints.
	public := map[string]bool{
		"/admin/login": true,
	}

	var checked int
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/admin") || public[route] {
			return nil
		}
		checked++

		// Concrete values for the URL params, so the request reaches the handler
		// rather than dying in the router.
		path := strings.NewReplacer(
			"{id}", "some-id", "{formID}", "some-id", "{subID}", "some-id",
			"{waitlistID}", "some-id", "{entryID}", "some-id", "{bid}", "some-id",
		).Replace(route)
		path = strings.TrimSuffix(path, "/*")

		req := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		// Unauthenticated, every admin route must refuse. A 302 to the login page
		// is the shape here; anything that renders is a bypass.
		if w.Code != http.StatusFound && w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d without a session.\n"+
				"An admin route outside the RequireAuth group serves the page to "+
				"anyone, and renders perfectly while doing it.", method, route, w.Code)
			return nil
		}
		if loc := w.Header().Get("Location"); w.Code == http.StatusFound && !strings.HasPrefix(loc, "/admin/login") {
			t.Errorf("%s %s redirected to %q, not the login page", method, route, loc)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}

	if checked < 30 {
		t.Fatalf("only %d admin routes walked; the table is not being read", checked)
	}
	t.Logf("checked %d admin routes", checked)
}

// TestPublicRoutesAreReachableWithoutASession is the other half. A guard that
// only ever demands authentication would be satisfied by locking everything,
// including the login page nobody could then reach.
func TestPublicRoutesAreReachableWithoutASession(t *testing.T) {
	t.Parallel()
	r, _ := testRouter(t)

	for _, tc := range []struct {
		method, path string
		wantNot      int
	}{
		{"GET", "/healthz", http.StatusFound},
		{"GET", "/admin/login", http.StatusFound},
		{"GET", "/static/app.css", http.StatusFound},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == tc.wantNot {
			t.Errorf("%s %s redirected to login; it must be reachable without a "+
				"session, or nobody can get one", tc.method, tc.path)
		}
	}
}
