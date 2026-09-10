// Package addr owns what counts as an email address in a submission, and who
// counts as its sender.
//
// It exists because that definition was previously split three ways — the HTTP
// handler decided validity, the store decided the stored form, and the matcher
// decided the comparison form — and a filter bypass was found in the seam
// between them in three consecutive review rounds, each one a layer beneath the
// last fix. There is now one definition, and this package is it.
//
// It is sealed under internal/screen/internal, so nothing outside the screening
// decision can construct, normalise or compare an address. That is a compiler
// rule, not a convention: the three-way split cannot be recreated.
package addr

import (
	"net/mail"
	"strconv"
	"strings"
)

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
func Canonical(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if addr, err := mail.ParseAddress(v); err == nil {
		if a, ok := shaped(ASCIILower(addr.Address)); ok {
			return a, true
		}
	}
	return shaped(ASCIILower(v))
}

// addressShaped is the conservative "is this an address at all" test. The dot
// requirement matters because allAddresses runs this over every field value of
// every submission: without it, arbitrary "a@b" tokens in prose would start
// counting as addresses.
func shaped(v string) (string, bool) {
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
func ASCIILower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// SenderValid reports whether a submission names exactly one well-formed sender.
//
// Absent is valid — not every form has an email field. Ambiguous is not: two
// fields both claiming to be the sender means the submitter picks which value is
// read, and every tie-break has a side they can land on.
//
// This parses rather than calling Canonical, and the difference is deliberate.
// Canonical answers "what is the comparable form" and requires a dot after the @
// because it runs over every field of every submission and must not treat prose
// tokens as addresses. Validity asks "did the visitor type an address", and an
// intranet form posting user@localhost is a real submission. Do not unify them.
func SenderValid(fields map[string]string) bool {
	value, state := SenderAddress(fields)
	switch state {
	case SenderNone:
		return true
	case SenderOne:
		_, err := mail.ParseAddress(value)
		return err == nil
	default:
		// SenderAmbiguous today, and anything added later. The permissive
		// outcome must never be the one a new state falls into by default.
		return false
	}
}
