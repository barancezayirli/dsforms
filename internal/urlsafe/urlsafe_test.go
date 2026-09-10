package urlsafe

import "testing"

// The rows here are chosen for the implementations they kill, not for coverage.
// Every one of them corresponds to a way of writing this check that looks right
// and is wrong — a bare url.IsAbs, a HasPrefix("/"), a case-sensitive host
// compare, raw u.Host equality, a HasSuffix domain match, or trusting userinfo.
// A redirect validator that passes only the obvious cases is the kind that gets
// bypassed the week after it ships.

func TestRelativePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"the built-in success page", "/success", true},
		{"a path with query and fragment", "/thanks?ref=a#top", true},
		{"root", "/", true},

		// url.Parse("//evil.example.net") yields Host=evil.example.net with an
		// empty scheme, so a check that only asks "is it absolute?" says no and
		// lets the browser go to another origin. This is the single most common
		// open-redirect bypass.
		{"protocol-relative is not a path", "//evil.example.net", false},
		{"protocol-relative with a path", "//evil.example.net/x", false},

		// Browsers normalise backslashes to slashes in the authority position;
		// Go does not. HasPrefix(raw, "/") accepts these.
		{"backslash after the slash", `/\evil.example.net`, false},
		{"double backslash", `\\evil.example.net`, false},

		{"absolute is not a path", "https://example.com/x", false},
		{"scheme-only", "javascript:alert(1)", false},
		{"bare word", "thanks", false},
		{"empty", "", false},
		{"whitespace is not a path", "  /thanks", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RelativePath(tc.in); got != tc.want {
				t.Errorf("RelativePath(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSameOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		raw, anchor string
		want        bool
	}{
		{"identical origins", "https://example.com/a", "https://example.com/thanks", true},
		{"different paths on one origin", "https://example.com/deep/page?q=1", "https://example.com/", true},

		// url.Parse lowercases the scheme but NOT the host, so a case-sensitive
		// comparison rejects a legitimate redirect.
		{"host case is not significant", "https://EXAMPLE.COM/a", "https://example.com/t", true},
		{"scheme case is not significant", "HTTPS://example.com/a", "https://example.com/t", true},

		// Raw u.Host equality treats these as different origins; they are not.
		{"default https port is implicit", "https://example.com:443/a", "https://example.com/t", true},
		{"default http port is implicit", "http://example.com:80/a", "http://example.com/t", true},

		// HasSuffix(host, "example.com") matches this. It is a different domain.
		{"suffix is not a match", "https://example.com.evil.net/a", "https://example.com/t", false},
		{"subdomain is a different origin", "https://sub.example.com/a", "https://example.com/t", false},

		// Everything before the @ is userinfo. A hand-rolled host extractor that
		// splits on "/" or takes the text after "://" reads example.com here.
		{"userinfo cannot impersonate the host", "https://example.com@evil.net/", "https://example.com/t", false},

		{"scheme must match", "http://example.com/a", "https://example.com/t", false},
		{"explicit non-default port differs", "https://example.com:8443/a", "https://example.com/t", false},

		// An anchor that is not an absolute http(s) URL can never match. This is
		// what makes an empty BASE_URL or an unconfigured form safe without a
		// special case at the call site.
		{"empty anchor matches nothing", "https://example.com/a", "", false},
		{"empty candidate matches nothing", "", "https://example.com/t", false},
		{"relative anchor matches nothing", "https://example.com/a", "/thanks", false},
		{"non-http anchor matches nothing", "javascript:x", "javascript:x", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SameOrigin(tc.raw, tc.anchor); got != tc.want {
				t.Errorf("SameOrigin(%q, %q) = %v, want %v", tc.raw, tc.anchor, got, tc.want)
			}
		})
	}
}

func TestRedirect(t *testing.T) {
	t.Parallel()

	const (
		configured = "https://customer.example/thanks"
		base       = "https://forms.example.com"
	)

	cases := []struct {
		name                        string
		requested, configured, base string
		wantTarget                  string
		wantRefused                 bool
	}{
		// Nothing requested: the operator's own setting, then the built-in page.
		{"no request falls back to the configured redirect", "", configured, base, configured, false},
		{"no request and nothing configured", "", "", base, "/success", false},
		{"no request, nothing configured, no base", "", "", "", "/success", false},

		// Allowed.
		{"a path on this instance", "/thanks", configured, base, "/thanks", false},
		{"a path with nothing configured", "/thanks", "", "", "/thanks", false},
		{"same origin as the configured redirect", "https://customer.example/other", configured, base, "https://customer.example/other", false},
		{"same origin as BASE_URL", "https://forms.example.com/x", "", base, "https://forms.example.com/x", false},

		// Refused: falls back, never errors.
		{"another origin entirely", "https://evil.example.net/phish", configured, base, configured, true},
		{"another origin with nothing configured", "https://evil.example.net/phish", "", base, "/success", true},
		{"protocol-relative", "//evil.example.net", configured, base, configured, true},
		{"javascript scheme", "javascript:alert(1)", configured, base, configured, true},
		{"data scheme", "data:text/html,<script>1</script>", configured, base, configured, true},
		{"userinfo trick", "https://customer.example@evil.net/", configured, base, configured, true},
		{"suffix trick", "https://customer.example.evil.net/", configured, base, configured, true},

		// With no anchors at all, only paths are possible.
		{"absolute refused when nothing is configured", "https://customer.example/x", "", "", "/success", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, refused := Redirect(tc.requested, tc.configured, tc.base)
			if target != tc.wantTarget || refused != tc.wantRefused {
				t.Errorf("Redirect(%q, %q, %q) = (%q, %v), want (%q, %v)",
					tc.requested, tc.configured, tc.base,
					target, refused, tc.wantTarget, tc.wantRefused)
			}
		})
	}
}

// TestRedirectNeverReturnsTheRequestedValueWhenRefused is the property behind
// the table, stated once so a future row cannot quietly weaken it.
func TestRedirectNeverReturnsTheRequestedValueWhenRefused(t *testing.T) {
	t.Parallel()

	hostile := []string{
		"https://evil.example.net/phish",
		"//evil.example.net",
		`/\evil.example.net`,
		"javascript:alert(1)",
		"data:text/html,x",
		"https://customer.example@evil.net/",
		"https://customer.example.evil.net/",
		"ftp://evil.example.net/",
	}
	for _, in := range hostile {
		for _, anchors := range [][2]string{
			{"https://customer.example/thanks", "https://forms.example.com"},
			{"", ""},
		} {
			target, refused := Redirect(in, anchors[0], anchors[1])
			if !refused {
				t.Errorf("Redirect(%q, %q, %q) accepted a hostile value", in, anchors[0], anchors[1])
			}
			if target == in {
				t.Errorf("Redirect(%q, …) returned the requested value despite refusing it", in)
			}
		}
	}
}

func TestHTTPScheme(t *testing.T) {
	t.Parallel()

	// Characterises the webhook rule this absorbed, so the extraction can be
	// shown to change nothing.
	cases := []struct {
		in   string
		want bool
	}{
		{"http://hooks.example/x", true},
		{"https://hooks.example/x", true},
		{"HTTPS://hooks.example/x", true},
		{"ftp://hooks.example/x", false},
		{"javascript:alert(1)", false},
		{"hooks.example/x", false},
		{"", false},
		{"://nonsense", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := HTTPScheme(tc.in); got != tc.want {
				t.Errorf("HTTPScheme(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestConfiguredRedirect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty is allowed — the field is optional", "", true},
		{"a path", "/thanks", true},
		{"an absolute url", "https://customer.example/thanks", true},
		{"http is allowed", "http://customer.example/thanks", true},

		{"a typo'd scheme", "htp://customer.example/thanks", false},
		{"a bare domain", "customer.example/thanks", false},
		{"javascript", "javascript:alert(1)", false},
		{"absolute with no host", "https:///thanks", false},
		{"protocol-relative", "//customer.example/thanks", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ConfiguredRedirect(tc.in); got != tc.want {
				t.Errorf("ConfiguredRedirect(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestOrigin covers what goes in the log line. Paths and queries are excluded on
// purpose: a redirect can carry personal data, and this repo does not log
// submission-adjacent values.
func TestOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"https://a.example/x?q=secret#f", "https://a.example"},
		{"https://a.example:8443/x", "https://a.example:8443"},
		{"http://a.example/", "http://a.example"},
		{"javascript:alert(1)", "javascript:"},
		{"/thanks", "(no origin)"},
		{"", "(no origin)"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := Origin(tc.in); got != tc.want {
				t.Errorf("Origin(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestOriginNeverLeaksThePath is the property the table above samples.
func TestOriginNeverLeaksThePath(t *testing.T) {
	t.Parallel()

	const secret = "tracking-token-9f83a"
	for _, in := range []string{
		"https://a.example/" + secret,
		"https://a.example/x?token=" + secret,
		"https://a.example/x#" + secret,
	} {
		if got := Origin(in); got != "https://a.example" {
			t.Errorf("Origin(%q) = %q, want just the origin", in, got)
		}
	}
}
