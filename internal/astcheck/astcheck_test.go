package astcheck

import "testing"

const storePkg = "github.com/barancezayirli/dsforms/internal/store"

// TestImportedAsReturnsEveryLocalName is the regression test for the bug that
// caused this package to exist.
//
// Two guards had their own copy of "what is this package imported as". One
// returned every local name; the other returned the first and stopped. Go lets a
// file import one package twice under different names, so the second version was
// defeated by adding a plain import beside an aliased one — and the guard built
// on it reported success while `Store *st.Store` put the whole store back.
//
// The single-name shape is the trap: it is the obvious implementation, it is
// correct for every file anyone writes by hand, and it is wrong for exactly the
// file an attacker on the code review writes.
func TestImportedAsReturnsEveryLocalName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "plain import uses the last path segment",
			src:  "package p\nimport \"" + storePkg + "\"",
			want: []string{"store"},
		},
		{
			name: "aliased import uses the alias",
			src:  "package p\nimport st \"" + storePkg + "\"",
			want: []string{"st"},
		},
		{
			name: "both at once — the shape that defeated the single-name version",
			src:  "package p\nimport (\n\t\"" + storePkg + "\"\n\tst \"" + storePkg + "\"\n)",
			want: []string{"store", "st"},
		},
		{
			name: "not imported at all",
			src:  "package p\nimport \"database/sql\"",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ImportedAs(File(t, tc.src), storePkg)
			if len(got) != len(tc.want) {
				t.Fatalf("ImportedAs returned %v, want %v.\nA matcher that resolves "+
					"fewer names than the file declares is blind to the rest.", keys(got), tc.want)
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("ImportedAs did not return %q; got %v", w, keys(got))
				}
			}
		})
	}
}

// Verify itself has no dedicated test, deliberately.
//
// Testing it needs a fake testing.T — a sub-test does not work, because a failing
// sub-test marks its parent failed — and a fake whose Fatalf does not actually
// stop execution tests something other than the real thing. The machinery would
// be larger and less trustworthy than what it checks.
//
// It is exercised on every run instead: TestConcreteStoreDetectorFires,
// TestUploadIsStagedBesideTheDatabase and their siblings all route through it,
// and each was seen to fail with its detector deliberately broken. A helper this
// small, called from that many places, is covered by its callers.

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
