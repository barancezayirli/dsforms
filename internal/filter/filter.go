// Package filter holds the operator's explicit overrides of the spam scorer:
// addresses, domains and networks that are always accepted or always held, plus
// custom keywords that extend the built-in list.
//
// The scorer in internal/spam is deliberately conservative and hardcoded. This
// package is the escape hatch for the cases it gets wrong — a customer whose
// legitimate mail keeps scoring, or a spammer whose payload keeps sliding under
// the threshold. Validation and matching live here; persistence lives in
// internal/store.
package filter

import (
	"fmt"
	"net"
	"net/mail"
	"strings"
	"time"
)

// Rule kinds and types. These strings are also the CHECK constraint values on
// the filter_rules table, so changing one means a migration.
const (
	KindBlock = "block"
	KindAllow = "allow"

	TypeEmail   = "email"
	TypeDomain  = "domain"
	TypeIP      = "ip"
	TypeCIDR    = "cidr"
	TypeKeyword = "keyword"
)

// Rule is one operator-defined override.
type Rule struct {
	ID        string
	Kind      string // KindBlock | KindAllow
	Type      string // TypeEmail | TypeDomain | TypeIP | TypeCIDR | TypeKeyword
	Value     string // normalised by Validate before storage
	Note      string // free-text reminder, e.g. "restored Mar 3", "office IP"
	Hits      int
	CreatedAt time.Time
}

// Validate checks a rule value against its type and returns the normalised form
// to store. Normalising on the way in is what lets matching be a plain string
// comparison rather than a parse per submission — and it is what makes the
// UNIQUE(kind, type, value) constraint meaningful, since "Example.COM" and
// "example.com" would otherwise both be storable.
func Validate(ruleType, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", fmt.Errorf("value is required")
	}

	switch ruleType {
	case TypeEmail:
		// Accepts both "bot@example.com" and "Bot <bot@example.com>", keeping
		// only the address. This is the same parser the submit handler uses to
		// validate an incoming email field, so the two agree on what an address
		// is.
		addr, err := mail.ParseAddress(v)
		if err != nil {
			return "", fmt.Errorf("not a valid email address: %s", v)
		}
		return strings.ToLower(addr.Address), nil

	case TypeDomain:
		d := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(v, "@"), "."))
		if !isHostname(d) {
			return "", fmt.Errorf("not a valid domain: %s", v)
		}
		return d, nil

	case TypeIP:
		ip := net.ParseIP(v)
		if ip == nil {
			return "", fmt.Errorf("not a valid IP address: %s", v)
		}
		// String() normalises: 2001:0DB8::0001 and 2001:db8::1 are one address
		// and must not be storable as two rules.
		return ip.String(), nil

	case TypeCIDR:
		_, network, err := net.ParseCIDR(v)
		if err != nil {
			return "", fmt.Errorf("not a valid CIDR range: %s", v)
		}
		// ParseCIDR masks host bits off, so 45.155.204.7/24 normalises to
		// 45.155.204.0/24 — the range the operator actually asked for.
		return network.String(), nil

	case TypeKeyword:
		k := strings.ToLower(v)
		if len(k) < 2 {
			return "", fmt.Errorf("keyword is too short to be meaningful: %q", v)
		}
		return k, nil
	}

	return "", fmt.Errorf("unknown rule type: %s", ruleType)
}

// isHostname reports whether s looks like a dotted hostname. Deliberately
// strict: a value with a scheme, a path, a space or no dot is far more likely
// to be a mistake than a domain the operator meant.
func isHostname(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '-' && i != 0 && i != len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

// Match reports the first rule that applies to a submission, allow rules first.
//
// Allow wins over block by construction rather than by ordering luck: an
// operator who allowlists an address after a false positive must not have it
// held again by a broader block rule they set up months earlier and forgot.
//
// Keyword rules are never matched here. They feed the scorer at the usual
// keyword weight instead, preserving the rule that a single keyword hit never
// holds a submission on its own.
func Match(rules []Rule, data map[string]string, ip string) (Rule, bool) {
	// Which addresses a rule may consider depends on its kind: an allow rule sees
	// only the sender field, a block rule sees every field. See senderAddresses.
	senderOnly := senderAddresses(data)
	anyField := allAddresses(data)

	for _, kind := range []string{KindAllow, KindBlock} {
		addrs := anyField
		if kind == KindAllow {
			addrs = senderOnly
		}
		for _, r := range rules {
			if r.Kind != kind || r.Type == TypeKeyword {
				continue
			}
			if matches(r, addrs, ip) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

func matches(r Rule, addrs []string, ip string) bool {
	switch r.Type {
	case TypeEmail:
		for _, addr := range addrs {
			if addr == r.Value {
				return true
			}
		}
	case TypeDomain:
		for _, addr := range addrs {
			at := strings.LastIndex(addr, "@")
			if at < 0 {
				continue
			}
			host := addr[at+1:]
			// Anchored on a dot so "example.ru" does not also match
			// "notexample.ru", which is a different organisation.
			if host == r.Value || strings.HasSuffix(host, "."+r.Value) {
				return true
			}
		}
	case TypeIP:
		if ip == "" {
			return false
		}
		parsed := net.ParseIP(ip)
		return parsed != nil && parsed.String() == r.Value
	case TypeCIDR:
		if ip == "" {
			return false
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return false
		}
		_, network, err := net.ParseCIDR(r.Value)
		return err == nil && network.Contains(parsed)
	}
	return false
}

// allAddresses collects every email-shaped value anywhere in a submission.
//
// Used for *block* rules only. Looking at all fields rather than one named
// "email" is right there: forms in the wild call it contact_address, reply_to,
// your-email and worse, and a spammer will not helpfully put their address in
// the field we happen to check.
func allAddresses(data map[string]string) []string {
	var out []string
	for _, v := range data {
		if addr, ok := emailShaped(v); ok {
			out = append(out, addr)
		}
	}
	return out
}

// senderAddresses collects only the canonical sender field.
//
// Used for *allow* rules, and the asymmetry with allAddresses is the whole
// point. Submitters choose their own field names, so scanning every field for an
// allow rule turns any mention of an allowlisted address into a skeleton key:
// appending one junk field bypasses the block list and all content scoring at
// once. The allowlisted address is usually the operator's own or a known
// customer's — guessable, not secret.
//
// "email" matched case-insensitively is the same field emailFieldValid in the
// submit handler treats as the sender, so the two agree on what a sender is.
func senderAddresses(data map[string]string) []string {
	var out []string
	for k, v := range data {
		if !strings.EqualFold(k, "email") {
			continue
		}
		if addr, ok := emailShaped(v); ok {
			out = append(out, addr)
		}
	}
	return out
}

// emailShaped reports whether a value looks like an address, returning it
// lowercased for comparison against a normalised rule value.
func emailShaped(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	ok := strings.Count(v, "@") == 1 && !strings.ContainsAny(v, " \t\n") && strings.Contains(v, ".")
	return v, ok
}

// Keywords returns the custom blocking keywords, for the scorer to add to its
// built-in list. Allow-kind keywords are meaningless and ignored.
func Keywords(rules []Rule) []string {
	var out []string
	for _, r := range rules {
		if r.Kind == KindBlock && r.Type == TypeKeyword {
			out = append(out, r.Value)
		}
	}
	return out
}
