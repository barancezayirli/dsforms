package handler

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"testing"

	"github.com/barancezayirli/dsforms/internal/astcheck"
)

const storePkg = "github.com/barancezayirli/dsforms/internal/store"

// parseHandlerPackage parses this package's non-test source once per test.
func parseHandlerPackage(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	return astcheck.Package(t, ".")
}

// structFields is astcheck.FieldNames under this package's older name.
func structFields(st *ast.StructType) (declared []string, embedded []string) {
	return astcheck.FieldNames(st)
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
// Handlers used to hold the store itself — most by embedding Base, three by
// declaring their own field — and could therefore reach every method on it,
// however few they called. Each now declares an interface naming exactly what it
// uses, so what a handler *can* touch is what it does touch.
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
		// Resolve the identifier the store is imported under in THIS file rather
		// than assuming "store". A second, aliased import of the same package is
		// legal Go, and `Store *st.Store` reinstates the whole surface while a
		// scan hardcoding "store" reports success.
		storeLocal := astcheck.ImportedAs(f, storePkg)
		if len(storeLocal) == 0 {
			continue
		}
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
				if !ok || !storeLocal[pkg.Name] || sel.Sel.Name != "Store" {
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

	// A count floor measures only that files were parsed; it cannot see a
	// predicate that has stopped matching. So the predicate is exercised against
	// a known-positive below, which is the part that actually keeps this honest —
	// one scan on this line of work passed while inspecting zero queries, because
	// it matched string literals when every query was a concatenation (c282125).
	if structs < 20 {
		t.Fatalf("only %d structs inspected; the scan is no longer finding them", structs)
	}
	t.Logf("inspected %d structs", structs)
}

// TestConcreteStoreDetectorFires is the positive control for the scan above.
//
// Without it, TestNoHandlerHoldsTheConcreteStore passing means either "no handler
// holds the concrete store" or "the detector no longer detects anything", and
// nothing distinguishes those. The aliased-import case is here because it is the
// one that actually got through: the first version resolved only the first import
// of a path, so adding a second under another name reinstated *store.Store with
// the suite green.
func TestConcreteStoreDetectorFires(t *testing.T) {
	t.Parallel()
	astcheck.Detector{
		Name:  "handler holds *store.Store",
		Match: holdsConcreteStore,
		Positive: map[string]string{
			"plain": `package handler
import "github.com/barancezayirli/dsforms/internal/store"
type H struct{ Store *store.Store }`,
			"aliased import": `package handler
import st "github.com/barancezayirli/dsforms/internal/store"
type H struct{ Store *st.Store }`,
			"aliased alongside the plain import": `package handler
import (
	"github.com/barancezayirli/dsforms/internal/store"
	st "github.com/barancezayirli/dsforms/internal/store"
)
type S interface{ GetForm(string) (store.Form, error) }
type H struct{ Store *st.Store }`,
		},
		Negative: map[string]string{
			"narrow interface": `package handler
import "github.com/barancezayirli/dsforms/internal/store"
type S interface{ GetForm(string) (store.Form, error) }
type H struct{ Store S }`,
			"unrelated pointer": `package handler
import "database/sql"
type H struct{ DB *sql.DB }`,
		},
	}.Verify(t)
}

// holdsConcreteStore reports whether f declares a struct field of type
// *store.Store, under whatever name the package is imported as.
//
// Extracted so the scan and its positive control run the same predicate. Two
// copies of a matcher is how one of them silently stops matching.
func holdsConcreteStore(f *ast.File) bool {
	local := astcheck.ImportedAs(f, storePkg)
	if len(local) == 0 {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if star, ok := field.Type.(*ast.StarExpr); ok {
				if sel, ok := star.X.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && local[id.Name] && sel.Sel.Name == "Store" {
						found = true
					}
				}
			}
		}
		return true
	})
	return found
}

// TestEveryStoreInterfaceMethodIsCalled stops the narrowed interfaces silently
// growing back.
//
// TestNoHandlerHoldsTheConcreteStore catches a field typed *store.Store. It does
// not catch SearchStore quietly growing from one method to twenty that nobody
// calls — and a reviewer demonstrated exactly that, adding ListUsers, DeleteForm
// and DB() to SearchStore with the whole suite green. Repeat seventy-five more
// times and the handler is back to reaching the entire store, with every guard
// passing and both interface doc comments still in place looking meaningful.
//
// Checked per owning field, not per package. The first version of this test
// asked only whether a method name was called *somewhere* in package handler,
// which meant adding ListUsers to SearchStore went unnoticed because
// UsersHandler calls it — the widening that matters is precisely the one that
// borrows another handler's method. So: for each type owning a storage
// interface, only calls of the form recv.Field.Method() inside that type's own
// methods count.
func TestEveryStoreInterfaceMethodIsCalled(t *testing.T) {
	t.Parallel()
	_, files := astcheck.Package(t, ".")

	ifaceMethods := map[string][]string{} // interface -> declared methods
	owner := map[string]ifaceOwner{}      // interface -> who holds it
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			switch typ := ts.Type.(type) {
			case *ast.InterfaceType:
				if !storeInterface.MatchString(ts.Name.Name) {
					return true
				}
				for _, m := range typ.Methods.List {
					for _, name := range m.Names {
						ifaceMethods[ts.Name.Name] = append(ifaceMethods[ts.Name.Name], name.Name)
					}
				}
			case *ast.StructType:
				for _, field := range typ.Fields.List {
					id, ok := field.Type.(*ast.Ident)
					if !ok || !storeInterface.MatchString(id.Name) {
						continue
					}
					for _, fn := range field.Names {
						owner[id.Name] = ifaceOwner{ts.Name.Name, fn.Name}
					}
				}
			}
			return true
		})
	}
	if len(ifaceMethods) < 10 {
		t.Fatalf("found only %d storage interfaces; the scan is not reading them",
			len(ifaceMethods))
	}

	// calls[type][field] is every method invoked through that field inside that
	// type's own methods.
	calls := map[string]map[string]map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
				continue
			}
			rt := fn.Recv.List[0].Type
			if star, ok := rt.(*ast.StarExpr); ok {
				rt = star.X
			}
			recvType, ok := rt.(*ast.Ident)
			if !ok {
				continue
			}
			var recvName string
			if len(fn.Recv.List[0].Names) > 0 {
				recvName = fn.Recv.List[0].Names[0].Name
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// recv.Field.Method(...)
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				field, ok := method.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				base, ok := field.X.(*ast.Ident)
				if !ok || base.Name != recvName {
					return true
				}
				if calls[recvType.Name] == nil {
					calls[recvType.Name] = map[string]map[string]bool{}
				}
				if calls[recvType.Name][field.Sel.Name] == nil {
					calls[recvType.Name][field.Sel.Name] = map[string]bool{}
				}
				calls[recvType.Name][field.Sel.Name][method.Sel.Name] = true
				return true
			})
		}
	}

	var checked int
	for iface, methods := range ifaceMethods {
		o, ok := owner[iface]
		if !ok {
			t.Errorf("%s is declared but no struct field holds it, so it constrains "+
				"nothing at all", iface)
			continue
		}
		used := calls[o.typ][o.field]
		for _, m := range methods {
			checked++
			if !used[m] {
				t.Errorf("%s declares %s, but %s.%s never calls it.\n"+
					"An interface method the owner does not invoke is store surface "+
					"granted for nothing, and it is how a one-method interface grows "+
					"back into the whole store while every other guard stays green.",
					iface, m, o.typ, o.field)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d interface methods inspected; the scan has stopped matching", checked)
	}
	t.Logf("inspected %d methods across %d storage interfaces", checked, len(ifaceMethods))
}

// ifaceOwner is the struct field a storage interface is held in.
type ifaceOwner struct{ typ, field string }

// storeInterface names the per-handler storage surfaces.
var storeInterface = regexp.MustCompile(`^(\w+Store|NavCounter)$`)
