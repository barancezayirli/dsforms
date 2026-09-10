package addr

import (
	"net/mail"
	"strings"
	"testing"
)

// foldASCII is the test's own, deliberately trivial, ASCII case fold.
//
// It exists so the property below does not depend on the function it is
// checking. An invariant expressed with the implementation's own helpers is
// self-consistent by construction and cannot see a bug inside them — which is
// exactly what happened on the first attempt at this: the allow-match property
// in FuzzDecide normalises with Canonical, so reintroducing a Unicode-unsafe
// fold left the property passing while the whole filter was bypassable again.
func foldASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// FuzzCanonicalIsInjectiveModuloASCIICase is the property the filter's whole
// security rests on.
//
// Two submissions may reduce to the same comparable address only if they are
// the same bytes once ASCII case is normalised. Anything looser means some
// spelling an operator never wrote collides with a rule they did — which is not
// hypothetical: `strings.ToLower` maps U+0130 to "i" and U+212A to "k", so
// "MİKE@works.com" and "MIKE@works.com" both collapsed onto "mike@works.com"
// and unlocked an allowlist entry for it.
//
// Stated this way the property is checkable without enumerating confusables. The
// Unicode tables can grow a new one tomorrow and this still holds or fails on
// its own terms — which is the difference between a property and a list of the
// cases somebody thought of. Three review rounds' worth of case lists is what
// this replaces.
func FuzzCanonicalIsInjectiveModuloASCIICase(f *testing.F) {
	// The historical collisions, plus honest pairs that must still agree.
	f.Add("MİKE@works.com", "mike@works.com") // U+0130
	f.Add("MIKE@works.com", "mike@works.com") // U+212A KELVIN SIGN
	f.Add("mike@workſ.com", "mike@works.com") // U+017F LATIN SMALL LETTER LONG S
	f.Add("MIKE@WORKS.COM", "mike@works.com") // honest: must collide
	f.Add("Mike <mike@works.com>", "mike@works.com")
	f.Add("a@b.com", "c@d.com")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, a, b string) {
		ca, oka := Canonical(a)
		cb, okb := Canonical(b)
		if !oka || !okb || ca != cb {
			return // not a collision; nothing to prove
		}

		// They canonicalise the same. The only licence for that is being the
		// same address written in different ASCII case — anything else is a
		// distinct address being treated as this one.
		//
		// Display-name form is the one legitimate exception: the parser
		// deliberately reduces "Mike <mike@works.com>" to the address it
		// contains, so compare on what the parser extracted rather than the
		// whole input.
		fa, fb := foldASCII(extractAddress(a)), foldASCII(extractAddress(b))
		if fa != fb {
			t.Fatalf("distinct addresses canonicalise the same:\n"+
				"  %q -> %q\n  %q -> %q\n"+
				"Both reduce to %q, but they are not the same bytes under ASCII case folding "+
				"(%q vs %q). An address the operator never allowlisted now matches one they did.",
				a, ca, b, cb, ca, fa, fb)
		}
	})
}

// extractAddress returns the bare address a value carries, using the same
// parser Canonical does, but *without* any case folding — so the fold itself
// stays outside the property's own machinery.
func extractAddress(v string) string {
	if a, err := mail.ParseAddress(strings.TrimSpace(v)); err == nil {
		return a.Address
	}
	return strings.TrimSpace(v)
}

// FuzzCanonicalFoldsAllASCIICase is the other half of the property above, and it
// exists because the first half is one-directional by construction.
//
// Injectivity fails only when two *distinct* addresses collide. Under-folding
// produces *fewer* collisions, so the more broken the fold is, the more strongly
// that property holds — it is structurally blind to the opposite error. Measured:
// changing ASCIILower's bound from b[i] <= 'Z' to b[i] < 'Z', so capital Z is
// never folded, survived the entire test suite and fifteen million executions of
// the injectivity fuzzer.
//
// That mutant is a live bypass. An operator's block rule typed "BOZ@x.com" stores
// as "boZ@x.com"; a submission of "boz@x.com" reduces to "boz@x.com", misses the
// rule, and is accepted. The rule looks correct on the rules screen forever.
//
// So: every ASCII letter must fold, checked against the test's own trivial fold
// rather than the implementation's.
func FuzzCanonicalFoldsAllASCIICase(f *testing.F) {
	f.Add("BOZ@x.com")
	f.Add("AZaz@x.com")
	f.Add("QUUX@EXAMPLE.COM")
	f.Add("Mike <MIKE@Works.com>")
	f.Add("MİKE@works.com")
	f.Add("")

	f.Fuzz(func(t *testing.T, v string) {
		got, ok := Canonical(v)
		if !ok {
			return
		}
		// Canonicalising an already-ASCII-folded value must reach the same
		// answer. If any letter escapes the implementation's fold, the two
		// disagree — which is exactly what an off-by-one on the A-Z range does.
		alsoOK, ok2 := Canonical(foldASCII(v))
		if !ok2 {
			t.Fatalf("Canonical(%q) succeeded but Canonical(foldASCII(%q)) = %q did not",
				v, v, foldASCII(v))
		}
		if got != alsoOK {
			t.Fatalf("the fold is incomplete:\n"+
				"  Canonical(%q)            = %q\n"+
				"  Canonical(foldASCII(%q)) = %q\n"+
				"Some ASCII letter is not being folded, so a rule stored with it "+
				"can never match a submission written in the other case.",
				v, got, v, alsoOK)
		}
	})
}
