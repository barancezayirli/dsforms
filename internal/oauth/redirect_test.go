package oauth

import "testing"

// A redirect URI is where an authorization code is delivered. Accepting a bad
// one hands a credential to whoever controls it, so the rows below are the ways
// a lenient check gets it wrong.

func TestValidateRedirectURI(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"https", "https://client.example.com/callback", true},
		{"https with a query", "https://client.example.com/cb?x=1", true},
		{"loopback ip, any port", "http://127.0.0.1:53682/callback", true},
		{"loopback ipv6", "http://[::1]:8080/cb", true},
		{"localhost", "http://localhost:3000/cb", true},

		// Plain http anywhere else puts the code on the wire.
		{"http to a remote host", "http://client.example.com/cb", false},
		// A hostname that merely starts with a loopback name is not loopback.
		{"localhost as a prefix", "http://localhost.evil.example/cb", false},
		{"127.0.0.1 as a prefix", "http://127.0.0.1.evil.example/cb", false},

		// RFC 6749 §3.1.2: the redirection endpoint must not include a fragment.
		{"fragment", "https://client.example.com/cb#frag", false},
		// Userinfo is how "https://trusted.example@evil.example" displays one
		// host on the consent page and delivers to another.
		{"userinfo", "https://trusted.example@evil.example/cb", false},

		{"relative", "/callback", false},
		{"protocol-relative", "//client.example.com/cb", false},
		{"javascript scheme", "javascript:alert(1)", false},
		{"custom scheme", "myapp://callback", false},
		{"empty", "", false},
		{"no host", "https:///cb", false},
		{"unparseable", "https://exa mple.com/%zz", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRedirectURI(tc.in)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateRedirectURI(%q) error = %v, want ok=%v", tc.in, err, tc.ok)
			}
		})
	}
}

func TestMatchRedirectURI(t *testing.T) {
	t.Parallel()

	registered := []string{"https://client.example.com/cb", "http://127.0.0.1:9000/cb"}

	cases := []struct {
		name string
		got  string
		want bool
	}{
		{"exact", "https://client.example.com/cb", true},
		{"second registered", "http://127.0.0.1:9000/cb", true},

		// Exact string comparison, per OAuth 2.1. Every relaxation below has been
		// an account-takeover bug somewhere.
		{"extra path segment", "https://client.example.com/cb/evil", false},
		{"prefix match", "https://client.example.com/c", false},
		{"added query", "https://client.example.com/cb?next=evil", false},
		{"different case host", "https://CLIENT.example.com/cb", false},
		{"trailing slash", "https://client.example.com/cb/", false},
		{"different loopback port", "http://127.0.0.1:9001/cb", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchRedirectURI(registered, tc.got); got != tc.want {
				t.Errorf("MatchRedirectURI(%q) = %v, want %v", tc.got, got, tc.want)
			}
		})
	}
}
