package main

import (
	"go/ast"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/barancezayirli/dsforms/internal/astcheck"
)

// handlerPkg is the import path whose composite literals this file checks.
const handlerPkg = "github.com/barancezayirli/dsforms/internal/handler"

// TestEveryStorageFieldIsWired catches the failure this branch introduced, and
// it is not hypothetical — it happened while making the change.
//
// Handlers used to hold Store *store.Store. There was exactly one thing to put
// there, so it was always put there. They now hold narrow interfaces, and most
// of them arrived in a single commit: a Go struct literal that omits a field
// zeroes it without complaint, so `go build ./...` reported success on a binary
// in which most handlers held a nil store. Nothing failed until a request
// arrived.
//
// The compiler cannot help — a nil interface is a valid value — and neither can
// the handler package's own tests, which construct their own handlers. The
// wiring exists only in main.go, so this is where it has to be checked.
//
// Which fields are required is read from main.go's own `var _ handler.X =
// (*store.Store)(nil)` block rather than inferred from how the interface is
// named. An earlier version of this test matched `^(\w+Store|NavCounter)$`, and
// renaming one interface off that convention dropped it out of the required set
// silently, taking its handler's wiring check with it. The assertion block is
// the authoritative list: it is compiler-checked, and it has to be edited when
// an interface is added, so it cannot quietly fall behind.
//
// Deliberately NOT required: Notifier, Webhook, Mailer and Broadcaster. Nil is a
// designed state for those — it means the feature is switched off — and
// main.go's deferred `waitlistSubmitHandler.Mailer = sendMailer` is correct
// rather than sloppy, because sendMailer is a *mail.Mailer and assigning it
// into the literal unconditionally would store a typed nil that passes every
// `!= nil` check and then panics. Requiring every interface field would force
// exactly that bug.
func TestEveryStorageFieldIsWired(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	_, mainFiles := astcheck.Package(t, ".")

	storeBacked := assertedStoreInterfaces(t, mainFiles)
	if len(storeBacked) < 10 {
		t.Fatalf("found only %d `var _ handler.X = (*store.Store)(nil)` assertions in "+
			"main.go; the scan is no longer reading them, so this test requires nothing",
			len(storeBacked))
	}

	required := requiredFields(t, fset, storeBacked)
	if len(required) < 10 {
		t.Fatalf("found storage fields on only %d handler types; the scan is no longer "+
			"finding them", len(required))
	}

	seen := map[string]bool{}
	for path, f := range mainFiles {
		local := astcheck.ImportedAs(f, handlerPkg)
		if len(local) == 0 {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Match the package by its resolved import path, not by the
			// identifier "handler": a second, aliased import of the same package
			// is legal Go and would otherwise hide every literal built through it.
			if id, ok := sel.X.(*ast.Ident); !ok || !local[id.Name] {
				return true
			}
			want, ok := required[sel.Sel.Name]
			if !ok {
				return true
			}
			seen[sel.Sel.Name] = true

			set := map[string]ast.Expr{}
			for _, elt := range lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok {
						set[k.Name] = kv.Value
					}
				}
			}
			for _, field := range want {
				val, present := set[field.name]
				if !present {
					t.Errorf("%s:%d handler.%s is constructed without %s (%s).\n%s",
						path, fset.Position(lit.Pos()).Line, sel.Sel.Name, field.name,
						field.why, wiringAdvice)
					continue
				}
				// `Store: nil` sets the key and satisfies a presence check while
				// being exactly the state the test exists to prevent.
				if id, ok := val.(*ast.Ident); ok && id.Name == "nil" {
					t.Errorf("%s:%d handler.%s sets %s to nil explicitly.\n%s",
						path, fset.Position(lit.Pos()).Line, sel.Sel.Name, field.name,
						wiringAdvice)
				}
			}
			return true
		})
	}

	// A set difference, not a count. Comparing len(seen) against len(required)
	// lets any handler constructed twice pay for another being absent entirely —
	// and main.go has more than one construction path.
	if missing := slices.Sorted(maps.Keys(missingFrom(required, seen))); len(missing) > 0 {
		t.Errorf("these handler types have storage fields but are never constructed in "+
			"main.go: %v.\nEach is either unwired, or built somewhere this test cannot "+
			"see — which is the same thing from the request's point of view.", missing)
	}
	t.Logf("checked %v", slices.Sorted(maps.Keys(seen)))
}

const wiringAdvice = "That compiles — an omitted or nil interface field is nil — and then panics " +
	"on the first request that touches storage. Set it in the literal; assigning it " +
	"afterwards is deliberately not enough, because it is invisible at the construction " +
	"site. If the dependency is genuinely optional, wire an explicit no-op implementation " +
	"rather than leaving it nil."

// storageField is one field a handler literal must set, and why.
type storageField struct {
	name string
	why  string // the interface type, for the failure message
}

func missingFrom(required map[string][]storageField, seen map[string]bool) map[string]bool {
	out := map[string]bool{}
	for name := range required {
		if !seen[name] {
			out[name] = true
		}
	}
	return out
}

// assertedStoreInterfaces reads the handler interfaces that main.go asserts
// *store.Store satisfies. That block is the authoritative list of what is
// backed by the store.
func assertedStoreInterfaces(t *testing.T, files map[string]*ast.File) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range files {
		local := astcheck.ImportedAs(f, handlerPkg)
		if len(local) == 0 {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "_" {
				return true
			}
			sel, ok := vs.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || !local[id.Name] {
				return true
			}
			// Only assertions whose right-hand side is *store.Store: the same
			// block also pins mail and webhook types, which are optional.
			if len(vs.Values) == 1 && mentionsStoreStore(vs.Values[0]) {
				out[sel.Sel.Name] = true
			}
			return true
		})
	}
	return out
}

func mentionsStoreStore(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "store" && sel.Sel.Name == "Store" {
			found = true
		}
		return true
	})
	return found
}

// requiredFields reports, per handler type, the fields a literal must set.
//
// Both the type's own storage-interface fields and any embedded struct that
// carries one: omitting `Base: base` zeroes Nav along with the templates and the
// secret key, compiles cleanly, and panics on the first render. An earlier
// version of this test looked only at directly-declared fields and passed on
// exactly that.
func requiredFields(t *testing.T, fset *token.FileSet, storeBacked map[string]bool) map[string][]storageField {
	t.Helper()
	_, files := astcheck.Package(t, "internal/handler")

	structs := map[string]*ast.StructType{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				structs[ts.Name.Name] = st
			}
			return true
		})
	}

	// direct[T] is T's own storage-interface fields.
	direct := map[string][]storageField{}
	for name, st := range structs {
		for _, field := range st.Fields.List {
			id, ok := field.Type.(*ast.Ident)
			if !ok || !storeBacked[id.Name] {
				continue
			}
			for _, fn := range field.Names {
				direct[name] = append(direct[name], storageField{fn.Name, "a " + id.Name})
			}
		}
	}

	out := map[string][]storageField{}
	for name, st := range structs {
		fields := slices.Clone(direct[name])
		for _, field := range st.Fields.List {
			if len(field.Names) != 0 {
				continue // not embedded
			}
			id, ok := field.Type.(*ast.Ident)
			if !ok || len(direct[id.Name]) == 0 {
				continue
			}
			fields = append(fields, storageField{
				id.Name,
				"embedded, and carries " + direct[id.Name][0].name,
			})
		}
		if len(fields) > 0 {
			slices.SortFunc(fields, func(a, b storageField) int { return strings.Compare(a.name, b.name) })
			out[name] = fields
		}
	}
	return out
}
