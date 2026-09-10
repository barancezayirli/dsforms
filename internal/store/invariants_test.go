package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// flattenQuery renders a string expression as text, replacing identifiers with
// their names, so `"SELECT "+heldColumns+" FROM submissions"` becomes
// `SELECT heldColumns FROM submissions`.
//
// Reconstructing the concatenation is the whole point: every real read in this
// package builds its query that way, so a scan that only inspected string
// literals would find nothing and pass while asserting nothing.
func flattenQuery(n ast.Expr) (string, bool) {
	switch e := n.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		return e.Name, true
	case *ast.CallExpr:
		// heldColumnsFor("s") and friends: keep the function name.
		if id, ok := e.Fun.(*ast.Ident); ok {
			return id.Name, true
		}
		return "?", true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, lok := flattenQuery(e.X)
		r, rok := flattenQuery(e.Y)
		if !lok || !rok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

// TestEverySubmissionReadUsesTheSharedColumnList guards the invariant that
// actually shipped a bug, rather than a list of the paths that exist today.
//
// store.Submission promises that every read populates every column. That was
// broken once: four of six paths hand-wrote a six-column SELECT, so an accepted
// row came back claiming it had never been notified, and a restored one reported
// score 0 while the database said 11. Fixing it routed every path through
// heldColumns and scanHeld.
//
// But "every path" was then guarded by a test naming four of them. A ninth path
// hand-writing its own column list is ordinary, reviewable-looking Go that the
// compiler cannot object to and that test cannot see. This scans the package's
// own source instead, so the guard cannot fall behind the code — the same move
// as spam.AllRules and its AST test.
func TestEverySubmissionReadUsesTheSharedColumnList(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing package source: %v", err)
	}

	var inspected int
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				for _, arg := range call.Args {
					q, ok := flattenQuery(arg)
					if !ok {
						continue
					}
					norm := strings.Join(strings.Fields(q), " ")
					lower := strings.ToLower(norm)
					if !strings.HasPrefix(lower, "select") && !strings.Contains(lower, "returning") {
						continue
					}
					if !strings.Contains(lower, "submissions") {
						continue
					}
					// Aggregates and single-column probes read no Submission and
					// have no columns to populate.
					if strings.Contains(lower, "count(") ||
						strings.Contains(lower, "select is_held") ||
						strings.Contains(lower, "submissions_fts") && !strings.Contains(norm, "heldColumns") && strings.Contains(lower, "rowid") {
						continue
					}
					inspected++
					if !strings.Contains(norm, "heldColumns") {
						pos := fset.Position(arg.Pos())
						t.Errorf("%s:%d builds a submissions read without heldColumns:\n\t%s\n"+
							"Use heldColumns/heldColumnsFor with scanHeld, or every quarantine "+
							"field on the returned Submission is silently zero.", path, pos.Line, norm)
					}
				}
				return true
			})
		}
	}

	// A scan that matches nothing passes while asserting nothing, which is worse
	// than no test. The count is the guard on the guard.
	if inspected < 5 {
		t.Fatalf("only %d submission reads inspected; the scan has stopped matching "+
			"the way queries are written and is no longer guarding anything", inspected)
	}
	t.Logf("inspected %d submission reads", inspected)
}
