// Package astcheck is the shared machinery behind this repo's AST guards.
//
// Several tests here assert structural properties the compiler will not: that no
// handler holds the concrete store, that every storage field is wired, that every
// submission read uses the shared column list. Each one parses Go source, walks
// it looking for a shape, and fails when it finds one.
//
// Each one also used to do that from scratch. There were six hand-rolled
// parser.Parse call sites across five files, three separate copies of "parse this
// package's non-test files", and two copies of "what is this package imported
// as" — which had drifted: one returned every local name, the other returned only
// the first, and a file may legally import one package twice. That second version
// let `import st ".../store"` reinstate *store.Store with the whole suite green.
// Two copies of a matcher is how one of them silently stops matching.
//
// The other half of the problem is that a scan reporting "inspected 47 structs"
// cannot distinguish "found nothing" from "no longer looks for anything". Count
// floors measure the denominator; they are blind to a predicate that has died.
// Verify is the answer: it runs a detector against sources that must trip it and
// sources that must not, so the detector itself is tested rather than trusted.
//
// It imports testing because it exists only for tests. Nothing outside a _test.go
// file imports it, so it is never linked into the binary.
package astcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// Package parses the non-test Go files in dir.
func Package(t *testing.T, dir string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("astcheck: parsing %s: %v", dir, err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			files[path] = f
		}
	}
	if len(files) == 0 {
		t.Fatalf("astcheck: parsed no source files in %s", dir)
	}
	return fset, files
}

// File parses one source string, for fixtures.
func File(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("astcheck: parsing fixture: %v\n%s", err, src)
	}
	return f
}

// ImportedAs returns every identifier path is imported under in f.
//
// A set, not one name: Go permits the same package to be imported more than once
// under different names in a single file, and a matcher that resolves only the
// first is defeated by adding a second. That is not hypothetical — it is the bug
// this package was extracted to stop repeating.
func ImportedAs(f *ast.File, path string) map[string]bool {
	out := map[string]bool{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			out[imp.Name.Name] = true
			continue
		}
		out[path[strings.LastIndex(path, "/")+1:]] = true
	}
	return out
}

// Detector is a structural property, expressed as a predicate over one file.
type Detector struct {
	// What the detector looks for, for failure messages.
	Name string
	// Match reports whether src exhibits the shape.
	Match func(*ast.File) bool
	// Positive sources must trip it; Negative sources must not. Keyed by a short
	// description, which is what a failure names.
	Positive map[string]string
	Negative map[string]string
}

// Verify exercises a detector against its fixtures.
//
// This is the control that a count floor cannot be. A guard that reports
// "inspected 47 structs" and passes may mean the property holds, or may mean the
// matcher no longer matches anything — and on this repo it has meant the latter
// more than once: a scan that inspected zero queries because it looked for string
// literals when every query was a concatenation, and a scan defeated by an
// import alias.
//
// Fixtures are cheap and they fail loudly. Every guard should have them; a
// detector nobody has watched fire is not a guard.
func (d Detector) Verify(t *testing.T) {
	t.Helper()
	if len(d.Positive) == 0 {
		t.Fatalf("astcheck: detector %q has no positive fixtures, so nothing "+
			"establishes that it can fire at all", d.Name)
	}
	if len(d.Negative) == 0 {
		t.Fatalf("astcheck: detector %q has no negative fixtures, so nothing "+
			"establishes that it discriminates rather than flagging everything", d.Name)
	}

	for name, src := range d.Positive {
		if !d.Match(File(t, src)) {
			t.Errorf("%s: did NOT fire on %q, which it must catch.\n"+
				"The guard built on this detector is passing without asserting "+
				"anything.\n\n%s", d.Name, name, src)
		}
	}
	for name, src := range d.Negative {
		if d.Match(File(t, src)) {
			t.Errorf("%s: fired on %q, which is legitimate code.\n"+
				"A guard that flags correct code gets disabled, and then guards "+
				"nothing.\n\n%s", d.Name, name, src)
		}
	}
}

// FieldNames returns a struct's directly-declared field names and the names of
// the types it embeds.
func FieldNames(st *ast.StructType) (declared, embedded []string) {
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

// Structs returns every struct type declared in f, keyed by type name.
func Structs(f *ast.File) map[string]*ast.StructType {
	out := map[string]*ast.StructType{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			out[ts.Name.Name] = st
		}
		return true
	})
	return out
}
