package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/astcheck"
	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/go-chi/chi/v5"
)

// alwaysHealthy is the health check for routers under test, which have no
// database behind them.
//
// An explicit func rather than a nil meaning "skip": a nil that quietly means
// something is how the sidebar's degraded banner went missing for two rounds.
func alwaysHealthy(context.Context) error { return nil }

// TestHealthzReportsAnUnusableDatabase is the regression test for "nothing
// detected it".
//
// A failed backup restore could leave the process holding a closed database
// handle — every route 500s, and only a restart fixes it. /healthz returned a
// hardcoded "ok" throughout, so no orchestrator, monitor or human was told. The
// endpoint reported that the HTTP server was listening, which it always is,
// right up until it is useless.
func TestHealthzReportsAnUnusableDatabase(t *testing.T) {
	t.Parallel()

	r := newRouter(func(context.Context) error {
		return errors.New("sql: database is closed")
	})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /healthz with an unusable database = %d, want %d.\n"+
			"A healthcheck that cannot fail cannot trigger a restart, and a restart "+
			"is the only thing that recovers this state.",
			w.Code, http.StatusServiceUnavailable)
	}
	if w.Body.String() == "ok" {
		t.Error(`GET /healthz body is still "ok" while the database is unusable`)
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	r := newRouter(alwaysHealthy)

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
	r := newRouter(alwaysHealthy)
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
	r := newRouter(alwaysHealthy)
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
	r := newRouter(alwaysHealthy)
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
	r := newRouter(alwaysHealthy)
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
	r := newRouter(alwaysHealthy)
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
	r := newRouter(alwaysHealthy)
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

// TestUploadFormsReachTheirHandler checks the whole seam an upload crosses, not
// the one link of it that broke last time.
//
// templates/backups.html posted its file as name="backup" while
// BackupHandler.Import read r.FormFile("file"), so database restore failed on
// every use from the Nocturne port (664bfbc) until it was fixed: the operator
// picked a .db, confirmed a prompt saying the change could not be undone, and
// got "No file uploaded."
//
// The handler suite stayed green throughout, because it builds its own multipart
// body and posts "file" — it encoded the handler's half of the contract and
// never the template's, leaving the seam between them owned by nobody.
//
// The first version of this test checked only that a file input's name appeared
// in SOME handler's FormFile call. That closes the reported instance and not the
// mechanism: it goes green again the moment any second upload field exists
// anywhere, and it is blind to the two other ways this same form breaks with the
// same symptom — a missing enctype (the browser posts only the filename) and an
// action pointing at a route that does not accept the post. So each upload form
// is now followed all the way through: enctype, then action to route, then route
// to handler method, then the field name that method actually reads.
func TestUploadFormsReachTheirHandler(t *testing.T) {
	t.Parallel()

	routes := postRoutes(t)
	reads := formFileNamesByMethod(t)

	forms := uploadForms(t)
	if len(forms) == 0 {
		t.Fatal("no upload forms found in templates; the scan is no longer matching them")
	}

	for _, f := range forms {
		where := fmt.Sprintf("templates/%s (form action=%q)", f.file, f.action)

		if !strings.Contains(f.enctype, "multipart/form-data") {
			t.Errorf("%s carries a file input but enctype is %q.\nWithout "+
				"multipart/form-data the browser posts the file NAME as an ordinary "+
				"field and no bytes at all, so FormFile returns ErrMissingFile and the "+
				"operator is told no file was uploaded.", where, f.enctype)
			continue
		}
		if f.field == "" {
			t.Errorf("%s has a file input with no name attribute, so it posts nothing", where)
			continue
		}

		method, ok := routes[normalizePath(f.action)]
		if !ok {
			t.Errorf("%s posts to a path with no POST route in main.go.\nThe request "+
				"is answered by the router, not the handler — a 405, after the operator "+
				"confirmed the action.", where)
			continue
		}
		if !reads[method][f.field] {
			t.Errorf("%s posts its upload as name=%q, but %s reads %v.\nThe upload "+
				"arrives empty and the operator is told the file is missing.",
				where, f.field, method, slices.Sorted(maps.Keys(reads[method])))
		}
	}
	t.Logf("checked %d upload form(s) against %d POST route(s)", len(forms), len(routes))
}

// uploadForm is one <form> in a template that contains a file input.
type uploadForm struct {
	file, action, enctype, field string
}

func uploadForms(t *testing.T) []uploadForm {
	t.Helper()
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	formRe := regexp.MustCompile(`(?s)<form\b[^>]*>.*?</form>`)
	fileRe := regexp.MustCompile(`<input[^>]*type="file"[^>]*>`)
	attr := func(tag, name string) string {
		m := regexp.MustCompile(name + `="([^"]*)"`).FindStringSubmatch(tag)
		if m == nil {
			return ""
		}
		return m[1]
	}

	var out []uploadForm
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, form := range formRe.FindAllString(string(body), -1) {
			input := fileRe.FindString(form)
			if input == "" {
				continue
			}
			openTag := form[:strings.Index(form, ">")+1]
			out = append(out, uploadForm{
				file:    e.Name(),
				action:  attr(openTag, "action"),
				enctype: attr(openTag, "enctype"),
				field:   attr(input, "name"),
			})
		}
	}
	return out
}

// postRoutes maps each POST route pattern in main.go to the handler method bound
// to it, as "TypeName.MethodName".
func postRoutes(t *testing.T) map[string]string {
	t.Helper()
	_, files := astcheck.Package(t, ".")

	// Handler variable -> type name, from `x := &handler.XHandler{...}`.
	varType := map[string]string{}
	for _, f := range files {
		local := astcheck.ImportedAs(f, handlerPkg)
		if len(local) == 0 {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			name, ok := as.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			rhs := as.Rhs[0]
			if u, ok := rhs.(*ast.UnaryExpr); ok {
				rhs = u.X
			}
			lit, ok := rhs.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := lit.Type.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && local[id.Name] {
					varType[name.Name] = sel.Sel.Name
				}
			}
			return true
		})
	}

	out := map[string]string{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Post" {
				return true
			}
			pat, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			path, err := strconv.Unquote(pat.Value)
			if err != nil {
				return true
			}
			h, ok := call.Args[1].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := h.X.(*ast.Ident)
			if !ok {
				return true
			}
			if typ, ok := varType[recv.Name]; ok {
				out[normalizePath(path)] = typ + "." + h.Sel.Name
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no POST routes in main.go; the scan is no longer matching them")
	}
	return out
}

// formFileNamesByMethod maps "TypeName.MethodName" to the FormFile names that
// method reads.
func formFileNamesByMethod(t *testing.T) map[string]map[string]bool {
	t.Helper()
	_, files := astcheck.Package(t, "internal/handler")

	out := map[string]map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
				continue
			}
			typ := fn.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			id, ok := typ.(*ast.Ident)
			if !ok {
				continue
			}
			key := id.Name + "." + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "FormFile" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return true
				}
				name, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if out[key] == nil {
					out[key] = map[string]bool{}
				}
				out[key][name] = true
				return true
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("found no FormFile call sites in internal/handler; the scan is no longer matching them")
	}
	return out
}

// normalizePath reduces a template action and a chi route pattern to a common
// form, so `/admin/forms/{{.Form.ID}}/x` and `/admin/forms/{id}/x` compare equal.
func normalizePath(p string) string {
	p = regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(p, "{}")
	p = regexp.MustCompile(`\{[^{}]+\}`).ReplaceAllString(p, "{}")
	return strings.TrimSuffix(p, "/")
}

// TestLandingPageShowsEveryScreenshot keeps the captures and the page that shows
// them in step, in both directions.
//
// docs/screenshots/ held five committed images that docs/index.html referenced
// none of: the Admin section described the screens in prose while the pictures
// of them sat unused in the repo. The reverse rots just as easily — a renamed or
// deleted capture leaves a broken image on the public page, which nothing here
// would otherwise notice.
//
// Same shape as the backups template posting a field name no handler read: two
// halves, each fine, and no owner for the join.
func TestLandingPageShowsEveryScreenshot(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile(filepath.Join("docs", "index.html"))
	if err != nil {
		t.Fatalf("read docs/index.html: %v", err)
	}

	referenced := map[string]bool{}
	for _, m := range regexp.MustCompile(`screenshots/([\w-]+\.png)`).FindAllStringSubmatch(string(page), -1) {
		referenced[m[1]] = true
	}
	if len(referenced) == 0 {
		t.Fatal("the landing page references no screenshots; the scan is not matching them")
	}

	entries, err := os.ReadDir(filepath.Join("docs", "screenshots"))
	if err != nil {
		t.Fatalf("read docs/screenshots: %v", err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			onDisk[e.Name()] = true
		}
	}
	if len(onDisk) == 0 {
		t.Fatal("no screenshots on disk; the scan is not reading the directory")
	}

	for name := range referenced {
		if !onDisk[name] {
			t.Errorf("the landing page shows screenshots/%s, which is not committed — "+
				"a broken image on the public page", name)
		}
	}
	for name := range onDisk {
		if !referenced[name] {
			t.Errorf("docs/screenshots/%s is committed but the landing page never "+
				"shows it, so it is weight in the repo doing no work", name)
		}
	}

	// Dimensions on every img, or the page reflows as each one arrives.
	imgs := regexp.MustCompile(`<img[^>]*screenshots/[^>]*>`).FindAllString(string(page), -1)
	if len(imgs) != len(referenced) {
		t.Errorf("found %d screenshot <img> tags for %d referenced files", len(imgs), len(referenced))
	}
	for _, tag := range imgs {
		for _, attr := range []string{"width=", "height=", "alt=", "loading="} {
			if !strings.Contains(tag, attr) {
				t.Errorf("a screenshot <img> is missing %s, which costs either layout "+
					"stability or accessibility:\n%s", attr, tag)
			}
		}
	}
	t.Logf("checked %d screenshots", len(referenced))
}

// TestUnknownSubcommandDoesNotStartTheServer pins a dispatch that used to fall
// through.
//
// The switch over os.Args[1] had no default, so anything it did not recognise
// carried on into normal startup: `dsforms --help` began serving, and so did
// `dsforms backupp create` or any typo in a deploy script. Starting a server is
// the least safe response available to "I do not understand this command" — the
// operator believes one thing happened while another did.
//
// AGENT.md §4: in a switch over a closed value set the safe outcome is never the
// fall-through; name every case and let the default deny.
func TestUnknownSubcommandDoesNotStartTheServer(t *testing.T) {
	t.Parallel()
	fset, files := astcheck.Package(t, ".")

	var found, hasDefault bool
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			// The dispatch is the switch over os.Args[1].
			idx, ok := sw.Tag.(*ast.IndexExpr)
			if !ok {
				return true
			}
			sel, ok := idx.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Args" {
				return true
			}
			found = true
			for _, c := range sw.Body.List {
				if cc, ok := c.(*ast.CaseClause); ok && cc.List == nil {
					hasDefault = true
				}
			}
			if !hasDefault {
				t.Errorf("main.go:%d switches on os.Args with no default clause.\n"+
					"An unrecognised subcommand then falls through and starts the "+
					"server, so a typo silently serves instead of reporting the typo.",
					fset.Position(sw.Pos()).Line)
			}
			return true
		})
	}
	if !found {
		t.Fatal("no switch over os.Args found; the scan is not reading the dispatch")
	}
}

// TestResponsiveRulesTargetClassesThatExist guards a failure mode with no
// symptom.
//
// The mobile header bug this was written for had two halves: an auto margin
// that was never reset, and a title block that CSS could not address at all
// because its <div> carried no class. The second half is the dangerous one — a
// rule written against a class nothing carries does not warn, does not disturb
// the desktop layout, and simply does nothing, so the fix ships and the bug
// stays. Width-gating makes it worse: the only place the dead rule would have
// been visible is the narrow viewport nobody rechecks.
//
// Scoped to @media blocks for that reason. Membership is checked against the
// exact class tokens templates and app.js apply, not against the file text: a
// substring test passes on prose, on href path segments and on hyphenated
// siblings, so `.nav` would be satisfied by class="nav-backdrop" and `.export`
// by href="/admin/backups/export".
func TestResponsiveRulesTargetClassesThatExist(t *testing.T) {
	t.Parallel()

	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	carried, err := carriedClasses()
	if err != nil {
		t.Fatal(err)
	}
	if len(carried) == 0 {
		t.Fatal("no class attributes found in any template — the extractor stopped " +
			"matching and this guard is checking nothing")
	}

	blocks := mediaBlocks(string(css))
	if len(blocks) == 0 {
		t.Fatal("app.css declares no @media blocks — either the stylesheet stopped " +
			"being responsive or the extractor stopped matching")
	}
	checked := 0
	for _, block := range blocks {
		for _, class := range cssClassSelectors(block.body) {
			checked++
			if !carries(carried, class) {
				t.Errorf("app.css %s styles .%s, but no template or app.js ever applies "+
					"that class — the rule is dead and whatever it was meant to fix is "+
					"still broken at that width", block.cond, class)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d class selectors found across %d @media blocks — the "+
			"selector extractor has stopped matching", checked, len(blocks))
	}
}

// carriedClasses returns every class token the templates and app.js apply.
//
// A template class attribute can be partly computed —
// class="flash-{{.Flash.Type}}" — so a token containing an action is kept as
// the literal prefix before it and matched as a prefix. That is deliberately
// permissive: this guard exists to catch a class nothing carries, not to police
// how one is spelled.
func carriedClasses() (map[string]bool, error) {
	out := map[string]bool{}

	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil, fmt.Errorf("read templates dir: %w", err)
	}
	attr := regexp.MustCompile(`class="([^"]*)"`)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		for _, m := range attr.FindAllStringSubmatch(string(body), -1) {
			for _, tok := range strings.Fields(m[1]) {
				out[tok] = true
			}
		}
	}

	// app.js applies classes the templates never spell — body.nav-open and
	// body.rail among them — and it applies them only through classList. Reading
	// every quoted identifier instead would harvest tag names, event names and
	// selectors: querySelector('form') would make a dead `.form` rule look live.
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		return nil, fmt.Errorf("read app.js: %w", err)
	}
	applied := regexp.MustCompile(`classList\.(?:add|remove|toggle|replace)\(\s*['"]([A-Za-z_][\w-]*)['"]`)
	for _, m := range applied.FindAllStringSubmatch(string(js), -1) {
		out[m[1]] = true
	}
	return out, nil
}

// carries reports whether class is applied somewhere, allowing a token that was
// truncated at a template action to match as a prefix.
//
// A token that is *entirely* an action — class="{{.Kind}}" — has no literal
// prefix, and treating an empty prefix as a wildcard would accept every class
// name ever written. Those tokens are skipped: a class only ever produced by a
// computed value cannot be verified from the template text either way, and a
// guard that accepts everything is the failure this whole test exists to catch.
func carries(carried map[string]bool, class string) bool {
	if carried[class] {
		return true
	}
	for tok := range carried {
		prefix := literalPrefix(tok)
		if prefix != tok && prefix != "" && strings.HasPrefix(class, prefix) {
			return true
		}
	}
	return false
}

func literalPrefix(token string) string {
	if i := strings.Index(token, "{{"); i >= 0 {
		return token[:i]
	}
	return token
}

type mediaBlock struct {
	cond string // the @media condition, for the failure message
	body string
}

// mediaBlocks returns each @media block's body. The at-rule is matched
// case-insensitively because CSS treats it that way, so a stylesheet written
// @MEDIA would otherwise be skipped in full without the count floor noticing —
// three of this file's four blocks would still clear it.
//
// It counts braces rather than matching a regexp because the blocks nest rules
// inside them, and it strips comments and quoted strings first: a brace inside url("a}b.png") or inside a
// prose comment would otherwise end the block early and silently drop every
// rule after it from the scan.
func mediaBlocks(css string) []mediaBlock {
	css = blankNonCode(css)
	var out []mediaBlock
	for _, at := range regexp.MustCompile(`(?i)@media[^{]*\{`).FindAllStringIndex(css, -1) {
		depth, end := 1, -1
		for i := at[1]; i < len(css) && depth > 0; i++ {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
				end = i
			}
		}
		if end < 0 {
			continue
		}
		out = append(out, mediaBlock{
			cond: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(css[at[0]:at[1]]), "{")),
			body: css[at[1]:end],
		})
	}
	return out
}

// blankNonCode replaces the contents of comments and quoted strings with
// spaces, preserving every byte offset so the result can be scanned as if it
// were the original. Braces and dots inside them stop counting: a comment
// mentioning "e.g." would otherwise be read as a selector naming .g.
func blankNonCode(css string) string {
	b := []byte(css)
	for i := 0; i < len(b); {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			j := i + 2
			for ; j+1 < len(b) && !(b[j] == '*' && b[j+1] == '/'); j++ {
				b[j] = ' '
			}
			i = j + 2
		case b[i] == '"' || b[i] == '\'':
			quote := b[i]
			j := i + 1
			for ; j < len(b) && b[j] != quote; j++ {
				if b[j] == '\\' && j+1 < len(b) {
					b[j] = ' '
					j++
				}
				b[j] = ' '
			}
			i = j + 1
		default:
			i++
		}
	}
	return string(b)
}

// cssClassSelectors returns the class names named in a block's selectors. It
// reads only the text before each rule's opening brace, so declarations — where
// a value like `1 1 100%` carries no dot anyway — are ignored.
func cssClassSelectors(block string) []string {
	var out []string
	seen := map[string]bool{}
	for _, rule := range strings.Split(block, "}") {
		selector, _, ok := strings.Cut(rule, "{")
		if !ok {
			continue
		}
		for _, m := range regexp.MustCompile(`\.([A-Za-z_][\w-]*)`).FindAllStringSubmatch(selector, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// TestMediaBlockExtractorsHandleTheCasesThatWouldSilenceThem exercises the two
// extractors against input that must trip them and input that must not.
//
// Both failures they guard against are silent by construction: a brace inside a
// url() truncates a block and every rule after it goes unscanned, while a dot
// inside a comment invents a class and fails the build with a nonsense message.
// Neither shows up as a failing assertion in the guard itself — the guard just
// quietly checks less, or checks the wrong thing.
func TestMediaBlockExtractorsHandleTheCasesThatWouldSilenceThem(t *testing.T) {
	t.Parallel()

	t.Run("blocks", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			css  string
			want []string // classes found across every block
		}{
			{
				name: "plain block",
				css:  `@media (max-width: 640px) { .a { z: 1 } .b { z: 2 } }`,
				want: []string{"a", "b"},
			},
			{
				name: "brace inside url() must not end the block",
				css:  `@media print { .a { background: url("a}b.png") } .after { z: 1 } }`,
				want: []string{"a", "after"},
			},
			{
				name: "brace inside a content string must not end the block",
				css:  `@media print { .a::after { content: "}" } .after { z: 1 } }`,
				want: []string{"a", "after"},
			},
			{
				name: "nested media keeps the inner rules",
				css:  `@media print { @media (min-width: 10px) { .inner { z: 1 } } .outer { z: 2 } }`,
				want: []string{"inner", "outer"},
			},
			{
				name: "rules outside any media block are not scanned",
				css:  `.desktop { z: 1 } @media print { .mobile { z: 2 } }`,
				want: []string{"mobile"},
			},
			{
				// CSS at-rules are case-insensitive. A case-sensitive extractor
				// skips the block silently, and a count floor does not catch it
				// while the file's other blocks still clear the floor.
				name: "at-rule casing does not hide a block",
				css:  `@MEDIA print { .shouty { z: 1 } }`,
				want: []string{"shouty"},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				var got []string
				seen := map[string]bool{}
				for _, b := range mediaBlocks(tc.css) {
					for _, c := range cssClassSelectors(b.body) {
						if !seen[c] {
							seen[c] = true
							got = append(got, c)
						}
					}
				}
				sort.Strings(got)
				want := append([]string(nil), tc.want...)
				sort.Strings(want)
				if !slices.Equal(got, want) {
					t.Errorf("classes = %v, want %v", got, want)
				}
			})
		}
	})

	t.Run("comments invent no classes", func(t *testing.T) {
		t.Parallel()
		css := `@media print { /* see fig.4, e.g. app.css and .not-a-rule */ .real { z: 1 } }`
		var got []string
		for _, b := range mediaBlocks(css) {
			got = append(got, cssClassSelectors(b.body)...)
		}
		if !slices.Equal(got, []string{"real"}) {
			t.Errorf("classes = %v, want [real] — prose in a comment was read as a selector", got)
		}
	})

	t.Run("condition is reported", func(t *testing.T) {
		t.Parallel()
		blocks := mediaBlocks(`@media (max-width: 640px) { .a { z: 1 } }`)
		if len(blocks) != 1 {
			t.Fatalf("found %d blocks, want 1", len(blocks))
		}
		if blocks[0].cond != "@media (max-width: 640px)" {
			t.Errorf("cond = %q — the failure message would not say which breakpoint", blocks[0].cond)
		}
	})
}

// TestCarriedClassesDistinguishesAppliedFromMentioned pins the predicate that
// decides whether a class exists.
//
// The version this replaces matched each class name as a word against the
// concatenated text of every template, which passes on any prose, href segment
// or hyphenated sibling that happens to contain it: `.nav` was satisfied by
// class="nav-backdrop", `.export` by href="/admin/backups/export", `.title` by
// a <title> element. Those are the rows that must now fail.
func TestCarriedClassesDistinguishesAppliedFromMentioned(t *testing.T) {
	t.Parallel()

	carried, err := carriedClasses()
	if err != nil {
		t.Fatal(err)
	}

	// Applied by a real template or by app.js.
	for _, class := range []string{"crumb-block", "header-actions", "search", "nav-backdrop", "nav-open", "rail"} {
		if !carries(carried, class) {
			t.Errorf(".%s is applied in this tree but the guard says it is not — "+
				"live rules would be reported as dead", class)
		}
	}

	// Present in the text, never applied as a class. Each was a false pass
	// under the substring predicate.
	for _, class := range []string{"nav", "export", "rules", "admin", "new", "title", "form", "user", "waitlist"} {
		if carries(carried, class) {
			t.Errorf(".%s is never applied as a class, but the guard accepts it — "+
				"a dead rule named .%s would pass unnoticed", class, class)
		}
	}
}
