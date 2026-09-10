package addr

import (
	"testing"
)

// TestCanonicalAddressFoldsOnlyASCII is a property test, not a case list.
//
// The property: two byte-distinct values may canonicalise to the same string
// only if they are genuinely the same address. strings.ToLower violates it,
// because Unicode case folding is not injective into ASCII — U+0130 (İ) lowers
// to "i" and U+212A (KELVIN SIGN) lowers to "k". Any allowlisted address
// containing i, k or s was therefore reachable by an address the operator never
// allowlisted.
//
// This is the third round in which this bypass has been found open, each time
// one layer beneath the previous fix: first the field name was scanned too
// broadly, then the name was made canonical but the value was not. Enumerating
// the confusables that happen to be known today would repeat that mistake, so
// the assertion is the property itself — for every hostile spelling, the
// canonical form must differ from the honest one.
func TestCanonicalAddressFoldsOnlyASCII(t *testing.T) {
	t.Parallel()

	const honest = "mike@works.com"
	hostile := []struct{ name, value string }{
		{"U+0130 capital I with dot above", "MİKE@works.com"},
		{"U+212A kelvin sign", "MIKE@works.com"},
		{"U+017F latin small letter long s", "mike@workſ.com"},
		{"U+0131 dotless i", "mıke@works.com"},
		{"fullwidth latin", "ＭＩＫＥ@works.com"},
		{"kelvin in the domain", "mike@worKs.com"},
	}

	for _, h := range hostile {
		t.Run(h.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Canonical(h.value)
			if ok && got == honest {
				t.Errorf("Canonical(%q) = %q — collides with the honest address; "+
					"an address the operator never allowlisted would match", h.value, got)
			}
		})
	}

	// The other direction: honest spellings must still canonicalise together, or
	// the fix would break real matching instead of hostile matching.
	for _, v := range []string{"mike@works.com", "MIKE@WORKS.COM", "Mike@Works.Com", "  mike@works.com  "} {
		got, ok := Canonical(v)
		if !ok || got != honest {
			t.Errorf("Canonical(%q) = %q, %v; want %q, true", v, got, ok, honest)
		}
	}
}

// TestSenderAddress tests the mechanism directly, which the round-2 regression
// test did not.
//
// That test asserted through Match(), and under a "first key wins" regression
// whether Match hits depends on which of two keys Go's randomised map iteration
// reaches first — so it caught its own bug in 30 runs out of 60. Asserting the
// state here is deterministic: under that regression every ambiguous case
// returns SenderOne, every time.
//
// This is also the §7 gap: SenderAddress is the branch's most security-critical
// export and had no direct test at all.
func TestSenderAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      map[string]string
		wantAddr  string
		wantState SenderState
	}{
		{"no email field is legal", map[string]string{"message": "hi"}, "", SenderNone},
		{"empty data", map[string]string{}, "", SenderNone},
		{"one sender", map[string]string{"email": "a@x.com"}, "a@x.com", SenderOne},
		{"one sender, any case", map[string]string{"EMAIL": "a@x.com"}, "a@x.com", SenderOne},
		{"one sender beside other fields", map[string]string{"email": "a@x.com", "zz": "b@y.com"}, "a@x.com", SenderOne},
		{"an empty value is still one claimant", map[string]string{"email": ""}, "", SenderOne},

		// Two fields both claiming to be the sender. Every tie-break — by case,
		// by sort order, by iteration — has a side the submitter can land on, so
		// the only safe answer is that we do not know.
		{"two case variants", map[string]string{"email": "a@x.com", "Email": "b@y.com"}, "", SenderAmbiguous},
		{"upper and lower", map[string]string{"EMAIL": "a@x.com", "email": "b@y.com"}, "", SenderAmbiguous},
		{"three variants", map[string]string{"email": "a@x.com", "Email": "b@y.com", "eMaIl": "c@z.com"}, "", SenderAmbiguous},
		{"no exact-case sender among them", map[string]string{"Email": "a@x.com", "EMAIL": "b@y.com"}, "", SenderAmbiguous},

		// Near-misses: not claimants at all, so they cannot manufacture ambiguity
		// to suppress a legitimate sender.
		{"padded key is not a sender", map[string]string{"email": "a@x.com", " email": "b@y.com"}, "a@x.com", SenderOne},
		{"email-ish key is not a sender", map[string]string{"email": "a@x.com", "email2": "b@y.com"}, "a@x.com", SenderOne},
		{"dotless i is not a case variant", map[string]string{"email": "a@x.com", "emaıl": "b@y.com"}, "a@x.com", SenderOne},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr, state := SenderAddress(tt.data)
			if state != tt.wantState {
				t.Errorf("state = %v, want %v", state, tt.wantState)
			}
			if addr != tt.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tt.wantAddr)
			}
		})
	}
}

// TestSenderAddressReturnsNothingWhenUnresolved pins the contract that makes
// every caller safe: `addr, _ := SenderAddress(data)` yields "" rather than an
// attacker-chosen value.
//
// Without this, someone returning a "best effort" address for the ambiguous case
// — for a log line, or to show the operator — silently reopens the bypass at
// every call site that ignores the state, and nothing would catch it.
func TestSenderAddressReturnsNothingWhenUnresolved(t *testing.T) {
	t.Parallel()
	for _, data := range []map[string]string{
		{},
		{"message": "hi"},
		{"email": "a@x.com", "Email": "b@y.com"},
		{"EMAIL": "a@x.com", "Email": "b@y.com", "email": "c@z.com"},
	} {
		addr, state := SenderAddress(data)
		if state != SenderOne && addr != "" {
			t.Errorf("SenderAddress(%v) = %q with state %v; a non-SenderOne state must return the empty string", data, addr, state)
		}
	}
}
