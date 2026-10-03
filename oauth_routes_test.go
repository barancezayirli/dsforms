package main

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/config"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func oauthRouter(t *testing.T, base string, oauthOn bool) (*chi.Mux, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "oauth.db")
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
			DBPath: dbPath, BaseURL: base,
			SecretKey: strings.Repeat("a", 32), RateBurst: 100, RatePerMinute: 600,
			MCPEnabled: true, MCPOAuth: oauthOn,
		},
		templates: templates,
	})
	return r, s
}

// TestOAuthRoutesExistOnlyWhenEnabled. Registration is open to the internet,
// so an instance that did not ask for OAuth must not be serving it — asserted
// on the route table, since a 404 could come from a registered route too.
func TestOAuthRoutesExistOnlyWhenEnabled(t *testing.T) {
	t.Parallel()

	for _, on := range []bool{false, true} {
		r, _ := oauthRouter(t, "https://example.com", on)
		found := map[string]bool{}
		_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(route, "/oauth/") || strings.HasPrefix(route, "/.well-known/oauth") {
				found[method+" "+route] = true
			}
			return nil
		})
		if !on && len(found) > 0 {
			t.Errorf("OAuth off, but registered: %v", found)
		}
		if on {
			for _, want := range []string{
				"POST /oauth/register", "POST /oauth/token", "GET /oauth/authorize", "POST /oauth/authorize",
				"GET /.well-known/oauth-authorization-server", "GET /.well-known/oauth-protected-resource/mcp",
			} {
				if !found[want] {
					t.Errorf("OAuth on, but %s is not registered (have %v)", want, found)
				}
			}
		}
	}
}

// TestMCPChallengePointsAtTheResourceMetadata. With OAuth on, the 401 is how a
// client discovers where to sign in; with it off, pointing anywhere would
// advertise a flow that goes nowhere.
func TestMCPChallengePointsAtTheResourceMetadata(t *testing.T) {
	t.Parallel()

	for _, on := range []bool{false, true} {
		r, _ := oauthRouter(t, "https://example.com", on)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, mcpRequest(initialize, ""))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("oauth=%v: status %d", on, w.Code)
		}
		got := w.Header().Get("WWW-Authenticate")
		const want = `resource_metadata="https://example.com/.well-known/oauth-protected-resource/mcp"`
		if on != strings.Contains(got, want) {
			t.Errorf("oauth=%v: WWW-Authenticate = %q", on, got)
		}
		if !strings.Contains(got, `realm="dsforms"`) {
			t.Errorf("oauth=%v: the realm went missing: %q", on, got)
		}
	}
}

var consentField = regexp.MustCompile(`<input type="hidden" name="([a-z_]+)" value="([^"]*)">`)

// TestOAuthInteropWithTheSDKClient drives the MCP Go SDK's own OAuth client
// through discovery, registration, consent, token exchange and a tools/list
// against the real router. It is the regression test for everything the unit
// tests cannot see: which metadata path the client reads, whether it accepts
// our iss, our registration response and our token response — the details on
// which a real connector either connects or silently gives up.
func TestOAuthInteropWithTheSDKClient(t *testing.T) {
	t.Parallel()

	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	r, s := oauthRouter(t, base, true)
	srv.Config.Handler = r
	srv.Start()
	t.Cleanup(srv.Close)

	admin, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateSession(admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookie := auth.CreateSessionCookie(session, base)
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// The operator's part: open the consent page signed in, approve read only.
	approve := func(ctx context.Context, args *mcpauth.AuthorizationArgs) (*mcpauth.AuthorizationResult, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
		req.AddCookie(cookie)
		resp, err := browser.Do(req)
		if err != nil {
			return nil, err
		}
		page := new(strings.Builder)
		_, _ = io.Copy(page, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("consent page: %d %s", resp.StatusCode, page)
		}
		form := url.Values{}
		for _, m := range consentField.FindAllStringSubmatch(page.String(), -1) {
			form.Set(m[1], html.UnescapeString(m[2]))
		}
		form.Set("action", "approve")
		form.Set("scopes", "read")
		form.Set("reach", "all")

		post, _ := http.NewRequestWithContext(ctx, "POST", base+"/oauth/authorize", strings.NewReader(form.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.AddCookie(cookie)
		resp, err = browser.Do(post)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || loc.Query().Get("code") == "" {
			t.Fatalf("approval redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		q := loc.Query()
		return &mcpauth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	}

	const redirect = "http://127.0.0.1:1/callback"
	handler, err := mcpauth.NewAuthorizationCodeHandler(&mcpauth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &mcpauth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:   "interop",
				RedirectURIs: []string{redirect},
				GrantTypes:   []string{"authorization_code", "refresh_token"},
			},
		},
		RedirectURL:              redirect,
		AuthorizationCodeFetcher: approve,
		RequestRefreshToken:      true,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationCodeHandler: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "interop", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             base + "/mcp",
		OAuthHandler:         handler,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect through OAuth: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "list_forms") {
		t.Errorf("tools = %v, want list_forms", names)
	}
	// Consent said read; the scopes reached the tool gate.
	if strings.Contains(joined, "mark_spam") || strings.Contains(joined, "delete_submission") {
		t.Errorf("a read-only grant was offered write or delete tools: %v", names)
	}

	grants, err := s.ListOAuthGrants(admin.ID)
	if err != nil || len(grants) != 1 || grants[0].ClientName != "interop" {
		t.Errorf("grants = %+v, %v", grants, err)
	}
}
