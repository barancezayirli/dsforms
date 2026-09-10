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
