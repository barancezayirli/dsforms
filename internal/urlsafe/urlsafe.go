// Package urlsafe decides which URLs this service is willing to hand to a
// browser or call out to.
//
// It exists because `_redirect` was an open redirect. A form submission could
// name any destination and the handler returned it verbatim in a Location
// header, so a page anywhere could bounce a visitor off this instance's domain
// to a phishing page — with the trust of the domain they had just submitted to.
//
// The rule cannot simply be "same origin as us", because `_redirect` is a
// documented feature whose whole point is sending the visitor to the customer's
// own thank-you page, which is a different origin by definition. The way out is
// that the operator has already told us that origin: Form.Redirect and
// Waitlist.Redirect are admin-only fields, unreachable from a submission. So a
// submitter chooses a *page*, and only within an origin an operator vouched for.
//
// This is also where the webhook URL check lives, which was previously two
// identical copies inline in the admin handler. One place decides what a URL is
// allowed to be.
package urlsafe

import (
	"net/url"
	"strings"
)

// DefaultRedirect is where a submission goes when nothing else is available:
// the built-in success page on this instance.
const DefaultRedirect = "/success"

// Redirect resolves where to send the browser after a submission.
//
// requested is submitter-controlled and therefore untrusted. configured is the
// form's or waitlist's admin-set Redirect, and base is BASE_URL; both are
// operator-set and act as anchors. It returns the destination and whether the
// requested value was refused.
//
// A refused value is not an error. By the time this runs the submission is
// screened and stored, and failing the request over a bad *destination* would
// destroy real mail to punish a bad query parameter — the trade AGENT.md makes
// the other way everywhere else. The caller logs the refusal and sends the
// visitor somewhere that works.
func Redirect(requested, configured, base string) (string, bool) {
	fallback := configured
	if fallback == "" {
		fallback = DefaultRedirect
	}
	if requested == "" {
		return fallback, false
	}
	if RelativePath(requested) || SameOrigin(requested, configured) || SameOrigin(requested, base) {
		return requested, false
	}
	return fallback, true
}

// RelativePath reports whether raw is a path on this server.
//
// The rejections matter more than the acceptances. "//evil.example.net" parses
// with an empty Scheme and a Host, so a check asking only "is this absolute?"
// calls it relative and sends the browser to another origin — the most common
// way this class of guard is bypassed. And browsers normalise a backslash in the
// authority position to a slash while Go does not, so "/\evil.example.net" is a
// path to strings.HasPrefix and another origin to Chrome.
func RelativePath(raw string) bool {
	if !strings.HasPrefix(raw, "/") {
		return false
	}
	// Protocol-relative, or the backslash variant browsers treat the same way.
	if strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "" && u.Host == "" && u.User == nil
}

// SameOrigin reports whether raw and anchor are absolute http(s) URLs sharing a
// scheme, host and effective port.
//
// It returns false whenever the anchor is not itself an absolute http(s) URL,
// which is what lets callers pass an empty BASE_URL or an unconfigured form
// redirect without a special case: an absent anchor matches nothing rather than
// matching everything.
func SameOrigin(raw, anchor string) bool {
	a, ok := origin(raw)
	if !ok {
		return false
	}
	b, ok := origin(anchor)
	if !ok {
		return false
	}
	return a == b
}

// HTTPScheme reports whether raw parses and names http or https.
//
// This is the webhook rule, unchanged from the two inline copies it replaces —
// deliberately scheme-only, so the extraction changes no behaviour. It does not
// require a host; tightening it is a separate decision from consolidating it.
func HTTPScheme(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// ConfiguredRedirect reports whether raw is acceptable as an operator-set
// redirect: empty, a path, or an absolute http(s) URL with a host.
//
// Looser than Redirect on purpose. The operator is the trust anchor, so this
// does not constrain which origin they choose — an instance that second-guesses
// its own administrator has no anchor left. What it catches is the typo that
// makes the anchor useless: "htp://site/thanks" or a bare "site.com/thanks" is
// stored happily, matches nothing, and then `_redirect` stops working with no
// visible cause.
func ConfiguredRedirect(raw string) bool {
	if raw == "" || RelativePath(raw) {
		return true
	}
	_, ok := origin(raw)
	return ok
}

// Origin returns a short, log-safe description of raw.
//
// Log lines get the origin and never the path: a redirect URL can carry a
// tracking token or an email address in its query, and this repo does not put
// submission-adjacent values into logs.
func Origin(raw string) string {
	if o, ok := origin(raw); ok {
		return o
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		return u.Scheme + ":"
	}
	return "(no origin)"
}

// origin returns the normalised scheme://host[:port] of an absolute http(s) URL.
//
// Normalisation is the whole job. url.Parse lowercases the scheme but not the
// host, so a case-sensitive comparison would reject a legitimate redirect typed
// in capitals; and the default port is written both ways, so raw Host equality
// treats "example.com" and "example.com:443" as different origins. Userinfo is
// refused outright rather than ignored: everything before the "@" is userinfo,
// so "https://example.com@evil.net/" reads as example.com to anything that
// splits the string by hand.
func origin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	if port := u.Port(); port != "" && !isDefaultPort(u.Scheme, port) {
		host += ":" + port
	}
	return u.Scheme + "://" + host, true
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}
