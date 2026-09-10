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
	"log"
	"net"
	"net/mail"
	"strconv"
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
		// Through canonicalAddress, which is also what every submission value is
		// reduced by. Storing a rule in a form matching cannot produce is a rule
		// that never fires, and that gap was a live bypass twice.
		addr, ok := canonicalAddress(v)
		if !ok {
			return "", fmt.Errorf("not a valid email address: %s", v)
		}
		return addr, nil

	case TypeDomain:
		// asciiLower, not strings.ToLower, for the same reason as addresses: the
		// domain half of a submission address is folded the same way, so folding
		// the rule differently would let a Unicode spelling match it.
		d := asciiLower(strings.TrimPrefix(strings.TrimPrefix(v, "@"), "."))
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

	case TypeKeyword:
		// Keyword rules feed the scorer instead of matching here; Match filters
		// them out before this is reached.
		return false
	}

	// Unreachable while the filter_rules CHECK constraint holds. It is called out
	// rather than left as a bare fallthrough because returning false here is
	// fail-open for a *block* rule: a rule the operator believes is protecting
	// them would silently match nothing.
	log.Printf("filter: rule %s has unknown type %q; matched nothing", r.ID, r.Type)
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
		if addr, ok := canonicalAddress(v); ok {
			out = append(out, addr)
		}
	}
	return out
}

// SenderState describes how well a submission identifies who sent it.
type SenderState int

const (
	// SenderNone means no field named "email". Legal: not every form has one.
	SenderNone SenderState = iota
	// SenderOne means exactly one, and its value is the sender.
	SenderOne
	// SenderAmbiguous means two or more fields claim to be the sender.
	SenderAmbiguous
)

// String names the state, so a log line or a failed assertion reads
// "SenderAmbiguous" rather than "2".
func (s SenderState) String() string {
	switch s {
	case SenderNone:
		return "SenderNone"
	case SenderOne:
		return "SenderOne"
	case SenderAmbiguous:
		return "SenderAmbiguous"
	}
	return "SenderState(" + strconv.Itoa(int(s)) + ")"
}

// SenderAddress resolves the single field that identifies the submitter,
// returning its raw value.
//
// The returned address is non-empty only when the state is SenderOne; every
// other state returns "". That is what makes `addr, _ := SenderAddress(data)`
// safe rather than a bypass — a caller who ignores the state gets nothing, not a
// guess. It is a contract, not an implementation detail: returning a "best
// effort" address for the ambiguous case, say for a log line, would silently
// reopen the hole every caller relies on this to close. It is the one definition of "the sender" in this
// codebase; both the allow-rule matcher here and the submit handler's email
// validation call it, so the two cannot drift.
//
// Ambiguity is unresolved, not resolved-arbitrarily. HTTP field names are
// case-sensitive, so "email" and "Email" are two distinct fields that one
// submission can carry at once. Picking a winner between them — by case, by sort
// order, by map iteration — hands an attacker the choice of which value we read,
// and every tie-break has a side they can land on. Two claimants therefore means
// we do not know who sent this.
//
// That matters because of what the caller does next. An allow rule is
// permissive: it skips the block list and all content scoring. A permissive rule
// must never fire on a guess, so SenderAmbiguous denies the match. A block rule
// is restrictive and keeps scanning every field via allAddresses — a spammer
// will not helpfully put their address in the field we check.
func SenderAddress(data map[string]string) (string, SenderState) {
	var (
		addr  string
		found int
	)
	for k, v := range data {
		if strings.EqualFold(k, "email") {
			found++
			addr = v
		}
	}
	switch found {
	case 0:
		return "", SenderNone
	case 1:
		return addr, SenderOne
	default:
		return "", SenderAmbiguous
	}
}

// senderAddresses returns the sender's address for *allow* rule matching, or
// nothing when the sender is absent or ambiguous.
//
// The asymmetry with allAddresses is the whole point. Submitters choose their
// own field names, so scanning every field for an allow rule turns any mention
// of an allowlisted address into a skeleton key: appending one junk field
// bypasses the block list and all content scoring at once. The allowlisted
// address is usually the operator's own or a known customer's — guessable, not
// secret.
func senderAddresses(data map[string]string) []string {
	raw, state := SenderAddress(data)
	if state != SenderOne {
		return nil
	}
	if addr, ok := canonicalAddress(raw); ok {
		return []string{addr}
	}
	return nil
}

// canonicalAddress reduces a value to the one form addresses are compared in,
// or reports that it is not an address.
//
// This is the single definition. Rule storage (Validate), submission matching
// (allAddresses, senderAddresses) and the submit handler's validation all go
// through it, because the package previously held three different answers to
// "what is an address" and each gap between them was a filter bypass:
//
//   - Validate parsed with mail.ParseAddress, which accepts RFC 5322
//     display-name form. Matching used a shape test that rejected any value
//     containing a space. So "Bot <bot@example.com>" validated, stored, and
//     displayed as the sender while being invisible to every block rule — one
//     token the spammer controls, defeating email and domain rules permanently.
//   - Both folded with strings.ToLower, which is not injective into ASCII:
//     U+0130 lowers to "i" and U+212A (Kelvin) to "k", so "MİKE@works.com"
//     matched an allow rule for "mike@works.com" and skipped the block list and
//     all scoring.
//
// Folding is ASCII-only for that reason. Two values match only if they are the
// same address in bytes once ASCII case is normalised — no Unicode spelling can
// collide with an operator's rule.
//
// The parser runs first so every form Validate accepts is also recognisable in a
// submission. The shape test remains as a fallback so unifying on the parser
// cannot lose block coverage the old test had for values the parser rejects.
func canonicalAddress(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if addr, err := mail.ParseAddress(v); err == nil {
		if a, ok := addressShaped(asciiLower(addr.Address)); ok {
			return a, true
		}
	}
	return addressShaped(asciiLower(v))
}

// addressShaped is the conservative "is this an address at all" test. The dot
// requirement matters because allAddresses runs this over every field value of
// every submission: without it, arbitrary "a@b" tokens in prose would start
// counting as addresses.
func addressShaped(v string) (string, bool) {
	ok := strings.Count(v, "@") == 1 &&
		!strings.ContainsAny(v, " \t\n") &&
		strings.Contains(v[strings.LastIndex(v, "@")+1:], ".")
	if !ok {
		return "", false
	}
	return v, true
}

// asciiLower lowercases A-Z and leaves every other byte alone.
//
// strings.ToLower is wrong here and the difference is the security property:
// Unicode case folding maps distinct characters onto ASCII ones, so it can turn
// an address the operator never wrote into one they did. Bytes in a multi-byte
// UTF-8 sequence are all >= 0x80, so they are never in the A-Z range and pass
// through untouched.
func asciiLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
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
