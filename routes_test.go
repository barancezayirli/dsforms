package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// mcpRouter builds the real route table with MCP enabled, and returns a token
// for it.
//
// It goes through routes() rather than mounting the handler directly, because
// everything this file is about — that the route exists, that the middleware is
// on it, that the body limit is exempted — is a property of the wiring rather
// than of the handler.
func mcpRouter(t *testing.T, scopes ...string) (*chi.Mux, *store.Store, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "mcp.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	admin, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}
	raw, _, err := s.CreateAPIToken(admin.ID, "test", scopes, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	r := routes(serverDeps{
		store: s,
		cfg: config.Config{
			DBPath: dbPath, BaseURL: "https://example.com",
			SecretKey: strings.Repeat("a", 32), RateBurst: 100, RatePerMinute: 600,
			MCPEnabled: true,
		},
		templates: templates,
	})
	return r, s, raw
}

// initialize is the MCP handshake, as the smallest request that proves the
// endpoint is really serving the protocol rather than merely answering 200.
const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func mcpRequest(body, token string) *http.Request {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// TestMCPRefusesEveryUnauthenticatedShape is the test routes_test.go could not
// give us for free.
//
// TestEveryAdminRouteRequiresAuth walks the table for routes under /admin, so
// /mcp is invisible to it — an endpoint that reads and deletes every submission
// in the database, outside the one guard that checks every other route. This is
// its replacement, and it asserts 401 specifically rather than "not 200",
// because a 500 would also be "not 200" and would mean something very different.
func TestMCPRefusesEveryUnauthenticatedShape(t *testing.T) {
	t.Parallel()
	r, s, valid := mcpRouter(t, "read")

	admin, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	revoked, revokedTok, err := s.CreateAPIToken(admin.ID, "revoked", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, err := s.DeleteAPIToken(admin.ID, revokedTok.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	expired, _, err := s.CreateAPIToken(admin.ID, "expired", []string{"read"}, -time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	tests := []struct {
		name  string
		token string
	}{
		{"no Authorization header at all", ""},
		{"an empty bearer token", " "},
		{"a token that was never issued", "dsf_0000000000000000"},
		{"a revoked token", revoked},
		{"an expired token", expired},
		{"the right shape, wrong value", valid + "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, mcpRequest(initialize, tt.token))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("POST /mcp with %s answered %d, want 401.\n"+
					"This endpoint reads and can delete every submission in the "+
					"database; anything but a refusal here is a data breach.\nBody: %.200q",
					tt.name, w.Code, w.Body.String())
			}
		})
	}

	// And the control: the valid token is actually served, or the test above
	// would pass just as well against an endpoint that refuses everyone.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, mcpRequest(initialize, valid))
	if w.Code != http.StatusOK {
		t.Fatalf("a valid token was refused with %d; the refusals above prove nothing.\nBody: %.300q",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dsforms") {
		t.Errorf("the initialize response does not name the server: %.300q", w.Body.String())
	}
}

// TestMCPRouteIsAbsentWhenDisabled. Off by default has to mean the route was
// never registered, not that it is registered and answering 401 — a live route
// for a disabled feature is a thing to keep auditing forever.
func TestMCPRouteIsAbsentWhenDisabled(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "off.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	r := routes(serverDeps{
		store: s,
		cfg: config.Config{
			DBPath: dbPath, BaseURL: "https://example.com",
			SecretKey: strings.Repeat("a", 32), RateBurst: 100, RatePerMinute: 600,
			// MCPEnabled deliberately left false.
		},
		templates: templates,
	})

	// Asserted on the route table, not just on a response: a 404 could equally
	// come from a registered route that happens to reject this request.
	err = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/mcp") {
			t.Errorf("%s %s is registered although MCP_ENABLED is unset", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, mcpRequest(initialize, "anything"))
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /mcp answered %d with MCP disabled, want 404", w.Code)
	}
}

// TestMCPTokenScopesReachTheTools is the wiring assertion between the store and
// the MCP server: a token's scopes column has to arrive at the tool gate.
//
// Without it, verifyMCPToken could return an empty Scopes on every call and
// every package-level test would still pass, because those supply their own
// token info.
func TestMCPTokenScopesReachTheTools(t *testing.T) {
	t.Parallel()

	listTools := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`

	tests := []struct {
		name       string
		scopes     []string
		wantSome   string
		wantNoneOf string
	}{
		{"read only", []string{"read"}, "list_submissions", "delete_submission"},
		{"read and write", []string{"read", "write"}, "mark_spam", "delete_submission"},
		{"everything", []string{"read", "write", "delete"}, "delete_submission", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, _, token := mcpRouter(t, tt.scopes...)

			// Stateless mode, so tools/list needs no prior session — but it does
			// need the protocol version header the handshake would have set.
			req := mcpRequest(listTools, token)
			req.Header.Set("MCP-Protocol-Version", "2025-06-18")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("tools/list answered %d: %.300q", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, tt.wantSome) {
				t.Errorf("a token scoped %v was not offered %q; scopes are not reaching the tools.\n%.400q",
					tt.scopes, tt.wantSome, body)
			}
			if tt.wantNoneOf != "" && strings.Contains(body, tt.wantNoneOf) {
				t.Errorf("a token scoped %v was offered %q", tt.scopes, tt.wantNoneOf)
			}
		})
	}
}

// TestMCPIsExemptFromTheGlobalBodyLimit. The 64KB cap is applied by middleware
// before any route runs, and a MaxBytesReader cannot be unwrapped afterwards —
// so an exemption has to be named in that middleware or the limit mcpserver
// sets is a setting that does nothing.
func TestMCPIsExemptFromTheGlobalBodyLimit(t *testing.T) {
	t.Parallel()
	r, _, token := mcpRouter(t, "read")

	// A call carrying more than 64KB of arguments. It is a nonsense search, but
	// what is asserted is that the body was read at all rather than truncated.
	big := strings.Repeat("a", 80*1024)
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":` +
		`{"name":"search_submissions","arguments":{"query":"` + big + `"}}}`

	req := mcpRequest(body, token)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusRequestEntityTooLarge || w.Code == http.StatusBadRequest {
		t.Fatalf("an 80KB MCP request answered %d; the global 64KB cap is still applied to /mcp", w.Code)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("an 80KB MCP request answered %d: %.200q", w.Code, w.Body.String())
	}
}
