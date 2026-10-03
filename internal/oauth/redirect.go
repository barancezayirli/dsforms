package oauth

import (
	"errors"
	"net"
	"net/url"
	"slices"
)

// ValidateRedirectURI decides whether a client may register raw as a place to
// deliver authorization codes.
//
// https anywhere, or http to a loopback address on any port (RFC 8252 §7.3, for
// desktop clients that listen locally). Nothing else: plain http to a remote
// host puts the code on the wire, and a custom scheme can be claimed by any app
// on the device.
func ValidateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("redirect_uri does not parse")
	}
	if u.Host == "" || u.Hostname() == "" {
		return errors.New("redirect_uri must be absolute")
	}
	// Userinfo is how "https://trusted.example@evil.example" shows one host on
	// a consent page and delivers to another.
	if u.User != nil {
		return errors.New("redirect_uri must not contain userinfo")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return errors.New("redirect_uri must not contain a fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return errors.New("redirect_uri must use https unless it is loopback")
	default:
		return errors.New("redirect_uri must use https")
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MatchRedirectURI reports whether got is one of the registered URIs, compared
// as exact strings. OAuth 2.1 requires exact matching; every relaxation — a
// prefix, a path suffix, a case-folded host — has been an account takeover
// somewhere.
func MatchRedirectURI(registered []string, got string) bool {
	return got != "" && slices.Contains(registered, got)
}
