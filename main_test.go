package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/go-chi/chi/v5"
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

// TestStandalonePagesExecute catches what TestTemplatesParse cannot: a template
// call to a definition that does not exist. Parsing accepts {{template "icons"}}
// happily and only fails when executed, so these pages have to actually render.
func TestStandalonePagesExecute(t *testing.T) {
	t.Parallel()

	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() failed: %v", err)
	}

	for _, name := range standalonePages {
		tmpl := templates[name]
		if tmpl == nil {
			t.Errorf("template %q was not registered", name)
			continue
		}
		// login.html needs data; the error pages take none. Executing with a
		// permissive map keeps this a structural check rather than a fixture.
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, map[string]any{}); err != nil {
			t.Errorf("executing %s: %v", name, err)
			continue
		}
		if buf.Len() == 0 {
			t.Errorf("%s rendered nothing", name)
		}
	}
}

// TestErrorPagesRenderStyled404 covers the upgrade errorPages performs over the
// plain-text fallback in newRouter. templates/404.html and 500.html existed in
// the repo before this redesign but were never parsed or routed.
func TestErrorPagesRenderStyled404(t *testing.T) {
	t.Parallel()

	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	r := newRouter()
	errorPages(r, templates)

	req := httptest.NewRequest("GET", "/nonexistent-route", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Nothing here") {
		t.Errorf("styled 404 not rendered; got %.200q", body)
	}
	if !strings.Contains(body, "/static/app.css") {
		t.Error("404 page does not link the stylesheet")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// TestLandingPageIsSelfContained guards docs/index.html, which GitHub Pages
// serves with no Go server behind it — nothing else in the test suite touches
// it, and a missing icon or a stray external request would only be noticed by a
// visitor.
func TestLandingPageIsSelfContained(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile("docs/index.html")
	if err != nil {
		t.Fatalf("read landing page: %v", err)
	}
	html := string(page)

	// Every referenced icon must be defined in the inlined sprite. A missing
	// one renders as nothing at all: no error, no broken-image glyph.
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`<symbol id="(ph-[a-z0-9-]+)"`).FindAllStringSubmatch(html, -1) {
		defined[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`href="#(ph-[a-z0-9-]+)"`).FindAllStringSubmatch(html, -1) {
		if !defined[m[1]] {
			t.Errorf("landing page references %q, which its sprite does not define", m[1])
		}
	}

	// The font is the only third-party request the page is allowed to make: no
	// analytics, no tracker, no CDN-hosted icon font or script.
	for _, m := range regexp.MustCompile(`(?:src|href)="(https?://[^"]+)"`).FindAllStringSubmatch(html, -1) {
		u := m[1]
		if strings.HasPrefix(u, "https://fonts.googleapis.com") ||
			strings.HasPrefix(u, "https://fonts.gstatic.com") ||
			strings.HasPrefix(u, "https://github.com/") {
			continue
		}
		t.Errorf("landing page makes an unexpected external request: %s", u)
	}

	// Every in-page anchor must resolve, or a nav link scrolls nowhere.
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([a-z0-9-]+)"`).FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`href="#([a-z0-9-]+)"`).FindAllStringSubmatch(html, -1) {
		if strings.HasPrefix(m[1], "ph-") {
			continue // icon reference, checked above
		}
		if !ids[m[1]] {
			t.Errorf("landing page links to #%s, which does not exist on the page", m[1])
		}
	}
}

// TestRecoveryRendersStyled500 covers the other half of errorPages. 500.html has
// been in templates/ since before the redesign and was parsed but never
// rendered — the recovery middleware wrote a plain string, so the styled page
// was dead weight.
func TestRecoveryRendersStyled500(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	r := newRouter()
	errorPages(r, templates)
	t.Cleanup(func() {
		// serverErrorPage is package-level, so restore the plain-text default
		// rather than leaking a template-backed renderer into other tests.
		serverErrorPage = func(w http.ResponseWriter) {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })

	req := httptest.NewRequest("GET", "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Something went wrong") {
		t.Errorf("styled 500 not rendered; got %.160q", w.Body.String())
	}
}

// TestFileUploadNamesReachAHandler catches a mismatch that no other test can see
// and that the browser reports as a plausible-looking error message.
//
// templates/backups.html posted its file as name="backup"; BackupHandler.Import
// read r.FormFile("file"). Restore therefore failed every single time it was
// used, from the feature's first commit — the operator picked a .db file,
// confirmed the "this cannot be undone" prompt, and got "No file uploaded."
//
// The handler test suite was green throughout, because it builds its own
// multipart body and posts "file": it encoded the handler's side of the contract
// and never the template's, so the two halves could disagree indefinitely with
// nothing to notice. That is the shape this repo keeps rediscovering — both ends
// individually tested, the seam between them owned by no one.
//
// Checked in the direction the bug runs: a template offering a field nobody
// reads is dead UI. The reverse is fine — a handler may read an upload posted by
// something other than a template.
func TestFileUploadNamesReachAHandler(t *testing.T) {
	t.Parallel()

	handlerSrc, err := os.ReadDir("internal/handler")
	if err != nil {
		t.Fatalf("read handler dir: %v", err)
	}
	read := map[string]bool{}
	formFile := regexp.MustCompile(`FormFile\("([^"]+)"\)`)
	for _, e := range handlerSrc {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile("internal/handler/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range formFile.FindAllStringSubmatch(string(body), -1) {
			read[m[1]] = true
		}
	}
	if len(read) == 0 {
		t.Fatal("found no FormFile call sites; the scan is no longer matching them")
	}

	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	input := regexp.MustCompile(`<input[^>]*type="file"[^>]*>`)
	nameAttr := regexp.MustCompile(`name="([^"]+)"`)

	checked := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, tag := range input.FindAllString(string(body), -1) {
			m := nameAttr.FindStringSubmatch(tag)
			if m == nil {
				t.Errorf("templates/%s has a file input with no name attribute, so it "+
					"posts nothing: %s", e.Name(), tag)
				continue
			}
			checked++
			if !read[m[1]] {
				t.Errorf("templates/%s posts its upload as name=%q, which no handler reads.\n"+
					"Handlers read: %v\nThe upload silently arrives empty and the operator "+
					"is told the file is missing.", e.Name(), m[1], keysOf(read))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no file inputs found in templates; the scan is no longer matching them")
	}
	t.Logf("checked %d file input(s) against %d FormFile name(s)", checked, len(read))
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
