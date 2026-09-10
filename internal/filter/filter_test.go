package filter

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		kind     string
		value    string
		want     string
		wantErr  bool
		errMatch string
	}{
		// email
		{name: "email is lowercased", kind: TypeEmail, value: "Spammer@Example.COM", want: "spammer@example.com"},
		{name: "email with display name is reduced to the address", kind: TypeEmail, value: "Bot <bot@example.com>", want: "bot@example.com"},
		{name: "email without @ is rejected", kind: TypeEmail, value: "notanemail", wantErr: true},
		{name: "empty email is rejected", kind: TypeEmail, value: "  ", wantErr: true},

		// domain
		{name: "domain is lowercased", kind: TypeDomain, value: "Example.RU", want: "example.ru"},
		{name: "domain strips a leading @", kind: TypeDomain, value: "@example.ru", want: "example.ru"},
		{name: "domain strips a leading dot", kind: TypeDomain, value: ".example.ru", want: "example.ru"},
		{name: "subdomain is fine", kind: TypeDomain, value: "mail.example.co.uk", want: "mail.example.co.uk"},
		{name: "domain without a dot is rejected", kind: TypeDomain, value: "localhost", wantErr: true},
		{name: "domain with a scheme is rejected", kind: TypeDomain, value: "http://example.ru", wantErr: true},
		{name: "domain with a space is rejected", kind: TypeDomain, value: "exa mple.ru", wantErr: true},

		// ip
		{name: "ipv4", kind: TypeIP, value: "45.155.204.7", want: "45.155.204.7"},
		{name: "ipv6 is normalised", kind: TypeIP, value: "2001:0DB8::0001", want: "2001:db8::1"},
		{name: "not an ip", kind: TypeIP, value: "45.155.204", wantErr: true},
		{name: "cidr in the ip field is rejected", kind: TypeIP, value: "45.155.204.0/24", wantErr: true},

		// cidr
		{name: "cidr is normalised to its network address", kind: TypeCIDR, value: "45.155.204.7/24", want: "45.155.204.0/24"},
		{name: "ipv6 cidr", kind: TypeCIDR, value: "2001:db8::/32", want: "2001:db8::/32"},
		{name: "bare ip in the cidr field is rejected", kind: TypeCIDR, value: "45.155.204.7", wantErr: true},

		// keyword
		{name: "keyword is lowercased and trimmed", kind: TypeKeyword, value: "  Crypto Pump  ", want: "crypto pump"},
		{name: "empty keyword is rejected", kind: TypeKeyword, value: "   ", wantErr: true},
		{name: "one-character keyword is rejected", kind: TypeKeyword, value: "a", wantErr: true},

		{name: "unknown type", kind: "nonsense", value: "x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Validate(tt.kind, tt.value)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate(%q, %q) = %q, want an error", tt.kind, tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(%q, %q) unexpected error: %v", tt.kind, tt.value, err)
			}
			if got != tt.want {
				t.Errorf("Validate(%q, %q) = %q, want %q", tt.kind, tt.value, got, tt.want)
			}
		})
	}
}

func rule(kind, typ, value string) Rule {
	return Rule{ID: kind + ":" + typ + ":" + value, Kind: kind, Type: typ, Value: value, CreatedAt: time.Now()}
}

func TestMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rules   []Rule
		data    map[string]string
		ip      string
		wantID  string
		wantHit bool
	}{
		{
			name:    "no rules, no match",
			data:    map[string]string{"email": "a@example.com"},
			ip:      "1.2.3.4",
			wantHit: false,
		},
		{
			name:    "email matches case-insensitively",
			rules:   []Rule{rule(KindBlock, TypeEmail, "bot@example.com")},
			data:    map[string]string{"email": "BOT@Example.com"},
			wantID:  "block:email:bot@example.com",
			wantHit: true,
		},
		{
			name:    "email rule does not match a different address",
			rules:   []Rule{rule(KindBlock, TypeEmail, "bot@example.com")},
			data:    map[string]string{"email": "real@example.com"},
			wantHit: false,
		},
		{
			name:    "domain matches the address suffix",
			rules:   []Rule{rule(KindBlock, TypeDomain, "example.ru")},
			data:    map[string]string{"email": "someone@example.ru"},
			wantID:  "block:domain:example.ru",
			wantHit: true,
		},
		{
			name:    "domain matches a subdomain",
			rules:   []Rule{rule(KindBlock, TypeDomain, "example.ru")},
			data:    map[string]string{"email": "someone@mail.example.ru"},
			wantID:  "block:domain:example.ru",
			wantHit: true,
		},
		{
			// The suffix check must be anchored on a dot, or "example.ru"
			// would block "notexample.ru" — a different organisation.
			name:    "domain does not match a lookalike suffix",
			rules:   []Rule{rule(KindBlock, TypeDomain, "example.ru")},
			data:    map[string]string{"email": "someone@notexample.ru"},
			wantHit: false,
		},
		{
			name:    "ip matches exactly",
			rules:   []Rule{rule(KindBlock, TypeIP, "45.155.204.7")},
			data:    map[string]string{},
			ip:      "45.155.204.7",
			wantID:  "block:ip:45.155.204.7",
			wantHit: true,
		},
		{
			name:    "cidr contains the ip",
			rules:   []Rule{rule(KindBlock, TypeCIDR, "45.155.204.0/24")},
			data:    map[string]string{},
			ip:      "45.155.204.99",
			wantID:  "block:cidr:45.155.204.0/24",
			wantHit: true,
		},
		{
			name:    "cidr does not contain an outside ip",
			rules:   []Rule{rule(KindBlock, TypeCIDR, "45.155.204.0/24")},
			data:    map[string]string{},
			ip:      "45.155.205.1",
			wantHit: false,
		},
		{
			// Keyword rules feed the scorer at the usual weight instead of
			// short-circuiting, so Match must ignore them entirely.
			name:    "keyword rules never match here",
			rules:   []Rule{rule(KindBlock, TypeKeyword, "casino")},
			data:    map[string]string{"message": "best casino ever"},
			wantHit: false,
		},
		{
			name: "any email-shaped field is considered, not just \"email\"",
			rules: []Rule{
				rule(KindBlock, TypeDomain, "example.ru"),
			},
			data:    map[string]string{"contact_address": "someone@example.ru"},
			wantID:  "block:domain:example.ru",
			wantHit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Match(tt.rules, tt.data, tt.ip)
			if ok != tt.wantHit {
				t.Fatalf("Match() hit = %v, want %v (got rule %+v)", ok, tt.wantHit, got)
			}
			if ok && got.ID != tt.wantID {
				t.Errorf("Match() matched %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

// TestMatchPrefersAllow is the precedence rule the whole feature rests on: an
// operator who allowlists an address after a false positive must not have it
// held again by a broader block rule they forgot about.
func TestMatchPrefersAllow(t *testing.T) {
	t.Parallel()
	rules := []Rule{
		rule(KindBlock, TypeDomain, "example.ru"),
		rule(KindAllow, TypeEmail, "real@example.ru"),
	}
	got, ok := Match(rules, map[string]string{"email": "real@example.ru"}, "")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Kind != KindAllow {
		t.Errorf("matched a %s rule (%s), want the allow rule to win", got.Kind, got.ID)
	}

	// And a non-allowlisted address on the same domain is still blocked.
	got, ok = Match(rules, map[string]string{"email": "bot@example.ru"}, "")
	if !ok || got.Kind != KindBlock {
		t.Errorf("bot@example.ru should still be blocked, got ok=%v rule=%+v", ok, got)
	}
}

func TestKeywords(t *testing.T) {
	t.Parallel()
	rules := []Rule{
		rule(KindBlock, TypeKeyword, "crypto pump"),
		rule(KindBlock, TypeEmail, "bot@example.com"),
		rule(KindAllow, TypeKeyword, "ignored"),
	}
	got := Keywords(rules)
	if len(got) != 1 || got[0] != "crypto pump" {
		t.Errorf("Keywords() = %v, want just the blocking keyword", got)
	}
}

// TestMatchAllowRulesOnlyConsiderTheSenderField is the regression test for a
// full spam bypass. Match scans every submitted field for an email-shaped
// value, and the submit handler accepts arbitrary field names — so appending a
// junk field holding an allowlisted address made Match return an allow rule,
// skipping the block list and all content scoring.
//
// The allowlisted address is typically the operator's own or a known customer's,
// so it is guessable rather than secret.
//
// The asymmetry is deliberate: scanning every field is right for a *block* rule
// (a spammer will not helpfully put their address in a field called "email"),
// and catastrophic for an *allow* rule (any mention becomes a skeleton key).
func TestMatchAllowRulesOnlyConsiderTheSenderField(t *testing.T) {
	t.Parallel()

	allow := []Rule{rule(KindAllow, TypeEmail, "vip@customer.com")}
	allowDomain := []Rule{rule(KindAllow, TypeDomain, "customer.com")}

	tests := []struct {
		name    string
		rules   []Rule
		data    map[string]string
		wantHit bool
	}{
		{
			name:    "allow matches the canonical sender field",
			rules:   allow,
			data:    map[string]string{"email": "vip@customer.com", "message": "hello"},
			wantHit: true,
		},
		{
			name:    "allow matches the sender field whatever its case",
			rules:   allow,
			data:    map[string]string{"Email": "VIP@Customer.com"},
			wantHit: true,
		},
		{
			name:  "an allowlisted address in an unrelated field does NOT match",
			rules: allow,
			data: map[string]string{
				"email":   "mallory@spam.example",
				"message": "[url=http://x]casino[/url]",
				"zz":      "vip@customer.com",
			},
			wantHit: false,
		},
		{
			name:    "the same trick with a domain rule does not work either",
			rules:   allowDomain,
			data:    map[string]string{"email": "mallory@spam.example", "note": "vip@customer.com"},
			wantHit: false,
		},
		{
			name:    "an allowlisted address in the message body alone does not match",
			rules:   allow,
			data:    map[string]string{"message": "please cc vip@customer.com"},
			wantHit: false,
		},

		// HTTP field names are case-sensitive, so "email" and "Email" are two
		// distinct fields that a single submission can carry at once. Matching
		// every field whose name case-insensitively equals "email" therefore left
		// the bypass open: the first fix narrowed the skeleton key from any field
		// name to a case variant, which is one keystroke, not a closed hole.
		//
		// Ambiguity is the resolution. Two fields both claiming to be the sender
		// means the sender is unknown, and a permissive rule must never fire on a
		// guess.
		{
			name:  "a second email-cased field does NOT unlock the allow rule",
			rules: allow,
			data: map[string]string{
				"email": "mallory@spam.example",
				"Email": "vip@customer.com",
			},
			wantHit: false,
		},
		{
			name:  "nor does an upper-cased one",
			rules: allow,
			data: map[string]string{
				"email": "mallory@spam.example",
				"EMAIL": "vip@customer.com",
			},
			wantHit: false,
		},
		{
			name:  "nor a mixed-cased one",
			rules: allow,
			data: map[string]string{
				"email": "mallory@spam.example",
				"eMaIl": "vip@customer.com",
			},
			wantHit: false,
		},
		{
			name:  "nor two variants with no exact-case sender at all",
			rules: allow,
			data: map[string]string{
				"Email": "mallory@spam.example",
				"EMAIL": "vip@customer.com",
			},
			wantHit: false,
		},
		{
			name:  "the same trick with a domain rule does not work either",
			rules: allowDomain,
			data: map[string]string{
				"email": "mallory@spam.example",
				"Email": "vip@customer.com",
			},
			wantHit: false,
		},
		{
			name:  "an ambiguous sender does not suppress a block rule",
			rules: []Rule{rule(KindBlock, TypeEmail, "mallory@spam.example")},
			data: map[string]string{
				"email": "mallory@spam.example",
				"Email": "vip@customer.com",
			},
			wantHit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Match(tt.rules, tt.data, "")
			if ok != tt.wantHit {
				t.Errorf("Match() hit = %v, want %v (matched %+v)", ok, tt.wantHit, got)
			}
		})
	}
}

// Block rules keep scanning every field — that is the half of the asymmetry
// that must not regress while fixing the other half.
func TestMatchBlockRulesStillScanEveryField(t *testing.T) {
	t.Parallel()

	rules := []Rule{rule(KindBlock, TypeDomain, "spam.example")}
	data := map[string]string{"contact_address": "bot@spam.example", "message": "hi"}

	got, ok := Match(rules, data, "")
	if !ok || got.Kind != KindBlock {
		t.Errorf("a block rule must match an address in any field; got ok=%v rule=%+v", ok, got)
	}
}

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
			got, ok := canonicalAddress(h.value)
			if ok && got == honest {
				t.Errorf("canonicalAddress(%q) = %q — collides with the honest address; "+
					"an address the operator never allowlisted would match", h.value, got)
			}
		})
	}

	// The other direction: honest spellings must still canonicalise together, or
	// the fix would break real matching instead of hostile matching.
	for _, v := range []string{"mike@works.com", "MIKE@WORKS.COM", "Mike@Works.Com", "  mike@works.com  "} {
		got, ok := canonicalAddress(v)
		if !ok || got != honest {
			t.Errorf("canonicalAddress(%q) = %q, %v; want %q, true", v, got, ok, honest)
		}
	}
}

// TestCanonicalAddressAcceptsEveryFormValidateAccepts pins the other half of the
// same root cause: the package had three definitions of "an address".
//
// Validate stored a rule using mail.ParseAddress, which accepts RFC 5322
// display-name form. Matching used emailShaped, which rejected any value
// containing a space. So "Bot <bot@example.com>" validated, stored, and rendered
// to the operator as the sender while being invisible to every block rule — one
// token the spammer fully controls, defeating both email and domain rules
// permanently.
//
// The property: anything Validate accepts as a rule must also be recognisable in
// a submission, and both must reduce to the same string.
func TestCanonicalAddressAcceptsEveryFormValidateAccepts(t *testing.T) {
	t.Parallel()

	forms := []string{
		"bot@example.com",
		"Bot <bot@example.com>",
		"<bot@example.com>",
		"BOT@EXAMPLE.COM",
		"\"Bot Sender\" <bot@example.com>",
	}
	for _, v := range forms {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			stored, err := Validate(TypeEmail, v)
			if err != nil {
				t.Fatalf("Validate(%q) = %v; a form the parser accepts must be storable", v, err)
			}
			matched, ok := canonicalAddress(v)
			if !ok {
				t.Fatalf("canonicalAddress(%q) did not recognise a form Validate stored", v)
			}
			if matched != stored {
				t.Errorf("Validate stored %q but matching reduces to %q — the two disagree "+
					"about what this address is, which is a rule that never fires", stored, matched)
			}
		})
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
