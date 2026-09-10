package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// storageIface matches the interfaces through which a handler reaches the
// database: the per-handler XStore surfaces, plus the shell's NavCounter.
var storageIface = regexp.MustCompile(`^(\w+Store|NavCounter)$`)

// TestEveryStorageFieldIsWired catches the failure this refactor introduced, and
// it is not hypothetical — it happened while making the change.
//
// Handlers used to hold Store *store.Store. There was exactly one thing to put
// there, so it was always put there. They now hold narrow interfaces, and eleven
// of those arrived in one commit: a Go struct literal that omits a field zeroes
// it without complaint, so `go build ./...` reported success on a binary in which
// every single handler held a nil store. Nothing failed until a request arrived.
//
// The compiler cannot help here — a nil interface is a valid value — and neither
// can the handler package's own tests, which construct their own handlers. The
// wiring only exists in main.go, so this is where it has to be checked.
//
// It reads the requirement out of the handler package rather than listing fields,
// so a twelfth handler added next year is covered the day it is declared.
func TestEveryStorageFieldIsWired(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	required := requiredStorageFields(t, fset)
	if len(required) < 10 {
		t.Fatalf("found storage fields on only %d handler types; the scan is no longer "+
			"finding them, so this test is asserting nothing", len(required))
	}

	files, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing main package: %v", err)
	}

	checked := 0
	for _, pkg := range files {
		for path, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				// handler.AdminHandler{...} — the only form main.go uses.
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "handler" {
					return true
				}
				want, ok := required[sel.Sel.Name]
				if !ok {
					return true
				}
				checked++

				set := map[string]bool{}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok {
							set[k.Name] = true
						}
					}
				}
				for _, field := range want {
					if set[field] {
						continue
					}
					t.Errorf("%s:%d handler.%s is constructed without %s.\n"+
						"That compiles — an omitted interface field is nil — and then "+
						"panics on the first request that touches storage. Set it in the "+
						"literal; assigning it afterwards is not enough for this check, "+
						"deliberately, because it is not visible at the construction site.",
						path, fset.Position(lit.Pos()).Line, sel.Sel.Name, field)
				}
				return true
			})
		}
	}

	if checked < len(required) {
		t.Errorf("only %d of %d handler types with storage fields are constructed here; "+
			"one is either unwired or built somewhere this test cannot see", checked, len(required))
	}
	t.Logf("checked %d construction sites against %d handler types", checked, len(required))
}

// requiredStorageFields reports, per handler type, the directly-declared fields
// whose type is one of the package's storage interfaces.
//
// Directly-declared only: a handler embeds Base and sets it as a whole with
// `Base: base`, so Base's own Nav is required of the Base literal and not of
// every handler that embeds it.
func requiredStorageFields(t *testing.T, fset *token.FileSet) map[string][]string {
	t.Helper()

	pkgs, err := parser.ParseDir(fset, "internal/handler", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing handler package: %v", err)
	}

	ifaces := map[string]bool{}
	structs := map[string]*ast.StructType{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				switch typ := ts.Type.(type) {
				case *ast.InterfaceType:
					if storageIface.MatchString(ts.Name.Name) {
						ifaces[ts.Name.Name] = true
					}
				case *ast.StructType:
					structs[ts.Name.Name] = typ
				}
				return true
			})
		}
	}
	if len(ifaces) < 10 {
		t.Fatalf("found only %d storage interfaces in internal/handler; expected one "+
			"per handler plus NavCounter", len(ifaces))
	}

	out := map[string][]string{}
	for name, st := range structs {
		for _, field := range st.Fields.List {
			id, ok := field.Type.(*ast.Ident)
			if !ok || !ifaces[id.Name] {
				continue
			}
			for _, fn := range field.Names {
				out[name] = append(out[name], fn.Name)
			}
		}
		sort.Strings(out[name])
	}
	return out
}
