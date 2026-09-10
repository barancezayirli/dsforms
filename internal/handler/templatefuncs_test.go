package handler

import (
	"bytes"
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/barancezayirli/dsforms/internal/astcheck"
	"github.com/barancezayirli/dsforms/internal/store"
)

// templatePackages are the source files that may build a template function map:
// main.go, which parses the production template set, and this package, whose
// tests parse their own. Anywhere else is out of scope for this guard and would
// need its own — noted because the scan is only as wide as this list.
var funcMapScanRoots = []string{"../../main.go", "."}

// funcMapDetector reports whether a file builds a template.FuncMap anywhere
// outside TemplateFuncs.
//
// It matches on the AST rather than on the text, because the text version of
// this guard did not work. It looked for the literal "template.FuncMap{", and a
// map built through an import alias — `import tmpl "html/template"`, then
// `tmpl.FuncMap{...}` — walked straight past it. That is not a hypothetical:
// astcheck exists in this repo because the same blindness let an aliased import
// reinstate *store.Store with the whole suite green, and its package doc says
// so. Writing a second string-matching detector after that is the mistake the
// package was extracted to prevent.
//
// Resolving the import gives the real property: any local name bound to
// html/template or text/template, used as the qualifier of a FuncMap composite
// literal. Package-level `var x = tmpl.FuncMap{...}` counts too, which is why
// this walks the whole file and skips only the one function allowed to build it.
func funcMapDetector() astcheck.Detector {
	return astcheck.Detector{
		Name: "template.FuncMap built outside TemplateFuncs",
		Match: func(f *ast.File) bool {
			names := astcheck.ImportedAs(f, "html/template")
			for n := range astcheck.ImportedAs(f, "text/template") {
				names[n] = true
			}
			if len(names) == 0 {
				return false
			}

			// The one function permitted to build the map. Skipping the node
			// rather than the file means a second literal elsewhere in
			// templatefuncs.go is still caught.
			var allowed ast.Node
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "TemplateFuncs" {
					allowed = fn
				}
			}

			// isFuncMap reports whether an expression names the FuncMap type.
			isFuncMap := func(e ast.Expr) bool {
				sel, ok := e.(*ast.SelectorExpr)
				if !ok {
					return false
				}
				pkg, ok := sel.X.(*ast.Ident)
				return ok && sel.Sel.Name == "FuncMap" && names[pkg.Name]
			}

			found := false
			ast.Inspect(f, func(n ast.Node) bool {
				if n == nil || (allowed != nil && n == allowed) {
					return false
				}
				switch node := n.(type) {
				case *ast.CompositeLit:
					// template.FuncMap{...}
					if isFuncMap(node.Type) {
						found = true
					}
				case *ast.CallExpr:
					// template.FuncMap(m) — a conversion, and
					// make(template.FuncMap) — neither is a composite literal,
					// and both were shown to walk past the earlier version.
					if isFuncMap(node.Fun) {
						found = true
					}
					if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "make" &&
						len(node.Args) > 0 && isFuncMap(node.Args[0]) {
						found = true
					}
				case *ast.TypeSpec:
					// type fm = template.FuncMap — an alias launders every later
					// use past a matcher keyed on the qualified name, so the
					// alias itself is the thing to refuse.
					if node.Assign.IsValid() && isFuncMap(node.Type) {
						found = true
					}
				}
				return true
			})
			return found
		},
		Positive: map[string]string{
			"plain import": `package p
import "html/template"
func build() template.FuncMap { return template.FuncMap{"add": nil} }`,

			// The case the string-matching version could not see.
			"aliased import": `package p
import tmpl "html/template"
func build() tmpl.FuncMap { return tmpl.FuncMap{"add": nil} }`,

			// Not inside any function, so a FuncDecl-only walk would miss it.
			"package-level var": `package p
import "html/template"
var funcs = template.FuncMap{"add": nil}`,

			"text/template": `package p
import "text/template"
func build() template.FuncMap { return template.FuncMap{"add": nil} }`,

			// A second map in the same file as the allowed one.
			"beside TemplateFuncs": `package p
import "html/template"
func TemplateFuncs() template.FuncMap { return template.FuncMap{"add": nil} }
func sneaky() template.FuncMap { return template.FuncMap{"add": nil} }`,

			// The four below were each confirmed to compile and to evade the
			// string-matching version of this guard. They are fixtures now so
			// that stays fixed.
			"conversion from a plain map": `package p
import "html/template"
var funcs = template.FuncMap(map[string]any{"add": nil})`,

			"make instead of a literal": `package p
import "html/template"
func build() template.FuncMap { return make(template.FuncMap) }`,

			"type alias launders the name": `package p
import "html/template"
type fm = template.FuncMap`,

			"aliased import and make together": `package p
import tmpl "html/template"
func build() tmpl.FuncMap { return make(tmpl.FuncMap) }`,
		},
		Negative: map[string]string{
			"the one allowed definition": `package p
import "html/template"
func TemplateFuncs() template.FuncMap { return template.FuncMap{"add": nil} }`,

			"calls it instead of building one": `package p
import "html/template"
func parse() *template.Template { return template.New("x").Funcs(TemplateFuncs()) }`,

			"a different FuncMap entirely": `package p
import "example.com/other"
func build() other.FuncMap { return other.FuncMap{"add": nil} }`,

			"no template import at all": `package p
func build() map[string]any { return map[string]any{"add": nil} }`,
		},
	}
}

// TestFuncMapDetectorFires is the positive control.
//
// A guard that reports "scanned 14 files, found nothing" cannot tell "the
// property holds" from "the matcher stopped matching". This runs the predicate
// against sources that must trip it and sources that must not, so the guard
// below is checking something even when it passes.
func TestFuncMapDetectorFires(t *testing.T) {
	t.Parallel()
	funcMapDetector().Verify(t)
}

// TestTemplateFuncMapHasOneDefinition is the guard on the duplication that made
// templatefuncs.go necessary.
//
// There were five template.FuncMap literals: the production map in main.go, one
// full copy of it in templates_test.go, and three single-entry stubs. The full
// copy is the dangerous one — a function added to production and not to it
// leaves the golden page tests rendering a template set that is not the one that
// ships, green while asserting nothing about the real pages. The stubs carry the
// opposite risk: a test template calling a function the harness never registered.
//
// One definition removes both.
func TestTemplateFuncMapHasOneDefinition(t *testing.T) {
	t.Parallel()

	detector := funcMapDetector()
	var offenders []string
	scanned := 0

	for _, path := range goFilesUnder(t, funcMapScanRoots) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		scanned++
		if detector.Match(astcheck.File(t, string(src))) {
			offenders = append(offenders, filepath.ToSlash(path))
		}
	}

	// The denominator. A scan that read two files reports no problems just as
	// confidently as one that read all of them.
	if scanned < 20 {
		t.Fatalf("scanned only %d Go files; the file walk has stopped finding "+
			"them and this guard is checking almost nothing", scanned)
	}

	if len(offenders) > 0 {
		t.Errorf("template.FuncMap is built outside TemplateFuncs in %d file(s):\n  %s\n\n"+
			"Call handler.TemplateFuncs() instead. A second copy of the map is how the\n"+
			"test harness starts rendering a different template set than production —\n"+
			"the golden page tests then pass while asserting nothing about what ships.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// goFilesUnder expands the scan roots into individual .go files. Test files are
// included deliberately: four of the five original copies lived in tests, so a
// guard that skipped them would have missed the whole problem.
func goFilesUnder(t *testing.T, roots []string) []string {
	t.Helper()
	var out []string
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("stat %s: %v", root, err)
		}
		if !info.IsDir() {
			out = append(out, root)
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read dir %s: %v", root, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				out = append(out, filepath.Join(root, e.Name()))
			}
		}
	}
	return out
}

// There is deliberately no test listing TemplateFuncs' keys.
//
// The obvious one — compare the map against a hand-written list of the same
// names — is a mirror of the literal it checks, kept in the same file, and it
// cannot know what the templates actually call: "add" is registered and no
// template uses it. Meanwhile realTemplates parses every real page with this
// map on every run, so a missing entry already fails loudly and by name with
// "function not defined". A second, hand-maintained copy of the key list is the
// duplication this file exists to remove.

// passwordMinimumInProse matches a password minimum stated as a number, in the
// phrasings a person actually writes.
//
// The first version matched only "minimum N". The server's own message on these
// same two pages says "must be at least %d characters" (internal/handler/users.go),
// which is therefore the single most likely sentence for someone to paste into
// the hint — and it slipped straight through. Matching the property rather than
// the one wording is the difference between closing the mechanism and closing the
// instance that was reported.
//
// "bcrypt cost 12" is deliberately not matched: it is a different number that
// happens to share a value, and banning every digit would make this guard the
// kind people delete.
var passwordMinimumInProse = regexp.MustCompile(
	`(?i)(min(?:imum)?(?:\s+of)?\s+\d+|at\s+least\s+\d+|no\s+fewer\s+than\s+\d+|\d+\s*\+?\s*characters?\s+(?:minimum|or\s+more))`)

// TestPasswordHintStatesNoNumberOfItsOwn is the guard on the defect that made
// minPassword necessary.
//
// The Nocturne port put "minimum 12 characters" into account.html and
// users_new.html a day before any minimum existed in the code; when one arrived
// it was 8. The markup and the check were written a day apart and disagreed
// immediately. Correcting the two literals would have left in place the mechanism
// that produced that — a number stated in a second location.
//
// So the templates may not name it: they call minPassword, which reads
// store.MinPasswordLength. This looks for a digit offered as the minimum rather
// than for the specific wrong value, because the next wrong value is a different
// number.
func TestPasswordHintStatesNoNumberOfItsOwn(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(templateDir)
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(templateDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		checked++
		for _, hit := range passwordMinimumInProse.FindAllString(string(b), -1) {
			t.Errorf("%s states a length minimum as a literal: %q\n"+
				"Use {{minPassword}} so the page cannot disagree with "+
				"store.MinPasswordLength — which is what it did.", e.Name(), hit)
		}
	}

	// The denominator, sized to the real directory rather than to a round number
	// well below it: a glob that silently matched half the templates would have
	// passed a floor of 10.
	if checked < 20 {
		t.Fatalf("scanned only %d templates; the directory glob has stopped "+
			"matching and this guard is checking almost nothing", checked)
	}
}

// TestPasswordMinimumPatternCatchesTheWordingsPeopleUse is the positive control
// for the regex above, which had to be widened once already.
func TestPasswordMinimumPatternCatchesTheWordingsPeopleUse(t *testing.T) {
	t.Parallel()

	mustMatch := []string{
		"minimum 12 characters",
		"minimum of 8 characters",
		"min 10 chars",
		// The server's own sentence, and the one the first version missed.
		"Password must be at least 12 characters.",
		"no fewer than 12 characters",
		"12 characters minimum",
		"12+ characters or more",
	}
	for _, s := range mustMatch {
		if !passwordMinimumInProse.MatchString(s) {
			t.Errorf("pattern missed %q, which states a minimum as a number", s)
		}
	}

	mustNotMatch := []string{
		"bcrypt cost 12 · minimum {{minPassword}} characters",
		"bcrypt at cost 12",
		"5 attempts, then a 15-minute lockout on this IP.",
		"1-6 of 6",
		"Auto-deleted after 30 days",
	}
	for _, s := range mustNotMatch {
		if passwordMinimumInProse.MatchString(s) {
			t.Errorf("pattern fired on %q, which is legitimate copy.\n"+
				"A guard that flags correct text gets deleted, and then guards nothing.", s)
		}
	}
}

// TestRenderedPasswordHintStatesTheEnforcedMinimum is the assertion this branch
// was actually about, and the one it originally shipped without.
//
// Two structural guards were not enough, and it is worth being precise about
// why. TestPasswordHintStatesNoNumberOfItsOwn proves the markup contains no
// literal. TestTemplateFuncsCoversWhatTheTemplatesCall proves the function is
// registered. Neither one renders anything, so neither notices if minPassword
// returns the wrong number — a review of this branch demonstrated exactly that
// by making it return MinPasswordLength-5 and watching the whole suite stay
// green. The defect being fixed is "the page states a minimum the server does
// not enforce", and that is a property of the rendered page, so it has to be
// asserted against the rendered page.
//
// This renders the two real templates and reads the number back out of the
// output, which catches every wording — including ones nobody anticipated —
// because it checks the value rather than the prose.
func TestRenderedPasswordHintStatesTheEnforcedMinimum(t *testing.T) {
	t.Parallel()
	templates := realTemplates(t)
	fixtures := populatedPageData()

	for _, name := range []string{"account.html", "users_new.html"} {
		t.Run(name, func(t *testing.T) {
			data, ok := fixtures[name]
			if !ok {
				t.Fatalf("%s has no fixture in populatedPageData", name)
			}
			var buf bytes.Buffer
			if err := templates[name].ExecuteTemplate(&buf, "base", data); err != nil {
				t.Fatalf("execute %s: %v", name, err)
			}
			body := buf.String()

			want := strconv.Itoa(store.MinPasswordLength)
			// The hint sentence, with the number in it. Matching the whole
			// phrase rather than a bare digit keeps this from passing on the
			// unrelated "bcrypt cost 12" beside it — which would otherwise make
			// the test green for any minimum that happened to equal the cost.
			hint := "minimum " + want + " characters"
			if !strings.Contains(body, hint) {
				t.Errorf("%s does not render %q.\nThe page states a password minimum "+
					"that is not store.MinPasswordLength (%s), which is the exact "+
					"disagreement this is meant to make impossible.", name, hint, want)
			}

			// The strength meter reads the same number from the markup. Without
			// this it silently keeps its own copy, which is where the number
			// lived before.
			attr := `data-strength-min="` + want + `"`
			if !strings.Contains(body, attr) {
				t.Errorf("%s does not render %s.\nThe password strength meter falls "+
					"back to a hardcoded 12 when the attribute is missing, so the "+
					"bars would light at a threshold the server does not use.", name, attr)
			}
		})
	}
}
