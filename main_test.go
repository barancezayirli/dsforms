package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/ratelimit"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	r := newRouter()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "ok" {
		t.Errorf("GET /healthz body = %q, want %q", w.Body.String(), "ok")
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	r := newRouter()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.header, func(t *testing.T) {
			t.Parallel()
			got := w.Header().Get(tt.header)
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestMaxBytesReader(t *testing.T) {
	t.Parallel()
	r := newRouter()
	// Add a test route that reads the body
	r.Post("/test-body", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	bigBody := make([]byte, 65*1024)
	req := httptest.NewRequest("POST", "/test-body", bytes.NewReader(bigBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := ratelimit.NewLimiter(2, 6, func() time.Time { return now })

	r := chi.NewRouter()
	r.With(rateLimitMiddleware(l)).Post("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// First 2 requests succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

func TestNotFoundHandler(t *testing.T) {
	t.Parallel()
	r := newRouter()
	req := httptest.NewRequest("GET", "/nonexistent-route", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Page not found") {
		t.Error("404 page content not rendered")
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()
	r := newRouter()
	r.Get("/panic-test", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	req := httptest.NewRequest("GET", "/panic-test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestRateLimitMiddlewareJSON(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := ratelimit.NewLimiter(1, 6, func() time.Time { return now })

	r := chi.NewRouter()
	r.With(rateLimitMiddleware(l)).Post("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Exhaust
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Rate limited with JSON Accept
	req2 := httptest.NewRequest("POST", "/test", nil)
	req2.Header.Set("Accept", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w2.Code)
	}
	var resp map[string]string
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["error"] != "too many requests" {
		t.Errorf("error = %q, want 'too many requests'", resp["error"])
	}
}

// TestTemplatesParse is the safety net this repo did not have. Every handler
// test builds its own inline fake templates, so nothing exercised the real
// files in templates/ — a typo in a page template was caught by neither
// `go build` nor `go test`, only by main() hitting log.Fatalf at runtime.
// Anything added to basePages or standalonePages is covered automatically.
func TestTemplatesParse(t *testing.T) {
	t.Parallel()

	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() failed: %v", err)
	}

	for _, name := range append(append([]string{}, basePages...), standalonePages...) {
		if templates[name] == nil {
			t.Errorf("template %q was not registered", name)
		}
	}
}

// TestBasePagesDefineContent catches a page template that parses fine but
// forgets its {{define "content"}} block — it would render as an empty shell in
// production with no error anywhere.
func TestBasePagesDefineContent(t *testing.T) {
	t.Parallel()

	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() failed: %v", err)
	}

	for _, name := range basePages {
		tmpl := templates[name]
		if tmpl == nil {
			t.Errorf("template %q was not registered", name)
			continue
		}
		if tmpl.Lookup("content") == nil {
			t.Errorf("template %q does not define a \"content\" block", name)
		}
		if tmpl.Lookup("base") == nil {
			t.Errorf("template %q lost the \"base\" definition when cloned", name)
		}
		if tmpl.Lookup("icons") == nil {
			t.Errorf("template %q is missing the \"icons\" sprite", name)
		}
	}
}

// TestIconSpriteCoversTemplateReferences fails when a template references an
// icon the sprite does not carry. Missing icons render as nothing at all — no
// console error, no broken-image glyph — so this is the only way to notice.
func TestIconSpriteCoversTemplateReferences(t *testing.T) {
	t.Parallel()

	sprite, err := templateFS.ReadFile("templates/icons.html")
	if err != nil {
		t.Fatalf("read icon sprite: %v", err)
	}
	have := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="(ph-[a-z0-9-]+)"`).FindAllStringSubmatch(string(sprite), -1) {
		have[m[1]] = true
	}
	if len(have) == 0 {
		t.Fatal("icon sprite defines no symbols")
	}

	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	used := regexp.MustCompile(`href="#(ph-[a-z0-9-]+)"`)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "icons.html" {
			continue
		}
		body, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range used.FindAllStringSubmatch(string(body), -1) {
			if !have[m[1]] {
				t.Errorf("templates/%s references %q, which the sprite does not define", e.Name(), m[1])
			}
		}
	}
}

// TestStylesheetCommentsBalance guards a failure mode that is invisible in the
// browser: an unbalanced comment delimiter silently swallows the rule that
// follows it, and CSS has no error reporting to tell you.
//
// This is a regression test. The stylesheet header documented its source as
// "_ds/nocturne-*/styles.css", and the */ in that glob closed the comment
// early — which discarded the @font-face rule immediately after it. The page
// still rendered, quietly falling back to system-ui, and the only visible
// symptom was a console warning about a preloaded font going unused.
func TestStylesheetCommentsBalance(t *testing.T) {
	t.Parallel()

	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}

	depth := 0
	for _, m := range regexp.MustCompile(`/\*|\*/`).FindAllStringIndex(string(css), -1) {
		if string(css[m[0]:m[1]]) == "/*" {
			depth++
			continue
		}
		depth--
		if depth < 0 {
			ctx := string(css[max(0, m[0]-70):m[1]])
			t.Fatalf("stray */ at byte %d closes a comment that was never opened — "+
				"the rule after it will be silently dropped.\ncontext: %q", m[0], ctx)
		}
	}
	if depth != 0 {
		t.Errorf("unterminated CSS comment: %d block(s) left open", depth)
	}
}

// TestFontPreloadMatchesFontFace keeps the preload hint in base.html and the
// @font-face src in app.css pointing at the same file. If they drift, the
// browser downloads the font twice — or preloads one it never uses.
func TestFontPreloadMatchesFontFace(t *testing.T) {
	t.Parallel()

	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	base, err := templateFS.ReadFile("templates/base.html")
	if err != nil {
		t.Fatalf("read base template: %v", err)
	}

	face := regexp.MustCompile(`@font-face\s*\{[^}]*src:\s*url\('([^']+)'\)`).FindSubmatch(css)
	if face == nil {
		t.Fatal("app.css declares no @font-face with a quoted src url")
	}
	preload := regexp.MustCompile(`rel="preload"\s+href="([^"]+)"`).FindSubmatch(base)
	if preload == nil {
		t.Fatal("base.html has no rel=preload font hint")
	}
	if string(face[1]) != string(preload[1]) {
		t.Errorf("font url mismatch:\n  @font-face src: %s\n  preload href:   %s", face[1], preload[1])
	}

	// And the file it names must actually be embedded.
	path := strings.TrimPrefix(string(face[1]), "/")
	if _, err := staticFS.ReadFile(path); err != nil {
		t.Errorf("@font-face points at %s, which is not embedded: %v", face[1], err)
	}
}
