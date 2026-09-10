package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// parseHandlerPackage parses this package's non-test source once per test.
func parseHandlerPackage(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing package source: %v", err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			files[path] = f
		}
	}
	if len(files) == 0 {
		t.Fatal("parsed no source files")
	}
	return fset, files
}

// structFields returns the directly-declared field names of a struct type, and
// the names of the types it embeds.
func structFields(st *ast.StructType) (declared []string, embedded []string) {
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			if id, ok := f.Type.(*ast.Ident); ok {
				embedded = append(embedded, id.Name)
			}
			continue
		}
		for _, n := range f.Names {
			declared = append(declared, n.Name)
		}
	}
	return declared, embedded
}

// TestNoStructShadowsAnEmbeddedField is the mechanism behind a bug that silently
// disabled a whole feature.
//
// overviewData embedded PageData, which carries Degraded, and also declared its
// own Degraded field. Go promotes the shallower field and html/template resolves
// exactly the same way, so base.html's {{if .Degraded}} bound to the local one
// and never saw the embedded one. The merge line wrote into the field the
// template could not reach. The result rendered as a completely plausible page:
// zeroed badges, zeroed KPIs, no banner — on the one screen the feature existed
// for.
//
// Nothing prevents a recurrence. go vet does not warn, the compiler is silent,
// and the page still renders. Eighteen structs embed PageData and PageData keeps
// gaining fields, so any page that wants its own Query, Title or Degraded
// re-creates it. The rule was written as a comment on PageData.Degraded; this is
// that comment as a check.
func TestNoStructShadowsAnEmbeddedField(t *testing.T) {
	t.Parallel()
	fset, files := parseHandlerPackage(t)

	// name -> its directly-declared fields
	fieldsOf := map[string][]string{}
	// name -> what it embeds
	embeds := map[string][]string{}
	// name -> position, for the error message
	pos := map[string]token.Position{}

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			declared, embedded := structFields(st)
			fieldsOf[ts.Name.Name] = declared
			embeds[ts.Name.Name] = embedded
			pos[ts.Name.Name] = fset.Position(ts.Pos())
			return true
		})
	}

	// promoted collects field names reachable through embedding, to a few levels.
	var promoted func(name string, depth int) []string
	promoted = func(name string, depth int) []string {
		if depth > 4 {
			return nil
		}
		var out []string
		for _, e := range embeds[name] {
			out = append(out, fieldsOf[e]...)
			out = append(out, promoted(e, depth+1)...)
		}
		return out
	}

	checked := 0
	for name := range fieldsOf {
		if len(embeds[name]) == 0 {
			continue
		}
		checked++
		inherited := map[string]bool{}
		for _, f := range promoted(name, 0) {
			inherited[f] = true
		}
		for _, f := range fieldsOf[name] {
			if inherited[f] {
				t.Errorf("%s declares %s, shadowing the promoted field of the same name (%s).\n"+
					"Go and html/template both resolve the shallower field, so writes to the "+
					"embedded one become invisible to templates — this exact shape silently "+
					"disconnected the degraded banner.", name, f, pos[name])
			}
		}
	}

	if checked < 10 {
		t.Fatalf("only %d embedding structs inspected; the scan is no longer finding them", checked)
	}
	t.Logf("inspected %d structs that embed another type", checked)
}

// TestShellActiveNamesAKnownNavGroup catches a typo that otherwise loses two
// things silently.
//
// PageData.Active is a bare string keyed against navGroups. A value that is not
// a key produces an empty breadcrumb *and* no highlighted sidebar item, on a
// page that otherwise renders perfectly — two silent losses, zero signal.
//
// A defined type would not close this: it still would not check that a constant
// has a navGroups entry, which is the same non-exhaustiveness that made the
// screen.Check type weaker than it looked. Every call site passes a literal, so
// checking membership at the call site is both cheaper and stricter.
func TestShellActiveNamesAKnownNavGroup(t *testing.T) {
	t.Parallel()
	fset, files := parseHandlerPackage(t)

	checked := 0
	for path, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Shell" || len(call.Args) != 4 {
				return true
			}
			lit, ok := call.Args[3].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				// A non-literal cannot be checked here; flag it so the guard
				// cannot be sidestepped by accident.
				t.Errorf("%s:%d passes a non-literal active group to Shell; this test can only "+
					"check literals", path, fset.Position(call.Pos()).Line)
				return true
			}
			active, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			checked++
			if _, ok := navGroups[active]; !ok {
				t.Errorf("%s:%d Shell(..., %q) — not a navGroups key, so this page renders "+
					"with no breadcrumb and no sidebar highlight",
					path, fset.Position(call.Pos()).Line, active)
			}
			return true
		})
	}

	if checked < 10 {
		t.Fatalf("only %d Shell call sites inspected; the scan is no longer matching them", checked)
	}
	t.Logf("inspected %d Shell call sites", checked)
}

// TestNoHandlerHoldsTheConcreteStore is what keeps this package's storage
// surfaces narrow.
//
// Every handler used to embed Base{Store *store.Store} and could therefore reach
// all seventy-nine methods on the store regardless of the five or twenty it
// actually called. Each now declares an interface naming exactly what it uses —
// SubmitStore is five methods, SearchStore is one — so what a handler *can* touch
// is what it does touch.
//
// Nothing in the compiler defends that. Changing a field back to *store.Store
// builds cleanly, every existing call site keeps working, and the narrowing is
// silently gone; the interface declaration stays in the file looking as though it
// still means something. That is precisely the regression this test exists to
// catch, and it is the same shape as the bug that motivated the whole refactor:
// a property everybody believed was enforced, enforced by nothing.
//
// main.go is exempt and not scanned — it is the composition root, it constructs
// the store, and it legitimately holds the concrete type.
func TestNoHandlerHoldsTheConcreteStore(t *testing.T) {
	t.Parallel()
	fset, files := parseHandlerPackage(t)

	structs := 0
	for path, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			structs++
			for _, field := range st.Fields.List {
				star, ok := field.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				sel, ok := star.X.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "store" || sel.Sel.Name != "Store" {
					continue
				}
				name := "(embedded)"
				if len(field.Names) > 0 {
					name = field.Names[0].Name
				}
				t.Errorf("%s:%d %s.%s is *store.Store — the whole store, not the "+
					"methods this type uses.\nDeclare an interface next to the type "+
					"naming just those methods, as the other handlers do, and assert "+
					"it in main.go's var block.",
					path, fset.Position(field.Pos()).Line, ts.Name.Name, name)
			}
			return true
		})
	}

	// A matcher that matches nothing passes while asserting nothing. Two AST
	// tests on this branch have already shipped in that state — one inspected
	// zero queries because every query was a concatenation rather than a literal.
	if structs < 20 {
		t.Fatalf("only %d structs inspected; the scan is no longer finding them", structs)
	}
	t.Logf("inspected %d structs", structs)
}
