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
